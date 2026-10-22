//go:build unix

package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// All assertions exercise a separately built executable, never the Tea model.
var executable string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "settings-executable-*")
	if err != nil {
		panic(err)
	}
	executable = filepath.Join(dir, "dev-box-settings")
	build := exec.Command("go", "build", "-o", executable, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build executable: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// environment builds a clean environment with the trusted owner; extra adds or
// overrides further variables (DEV_BOX_*, TERM, NO_COLOR...).
func environment(owner string, extra ...string) []string {
	var env []string
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "DEV_BOX_") || strings.HasPrefix(value, "TERM=") ||
			strings.HasPrefix(value, "NO_COLOR=") || strings.HasPrefix(value, "KUBERNETES_") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "TERM=xterm-256color")
	env = append(env, "DEV_BOX_AUTHENTICATED_OWNER="+owner)
	return append(env, extra...)
}

type session struct {
	cmd      *exec.Cmd
	terminal *os.File
	mu       sync.Mutex
	output   bytes.Buffer
	done     chan struct{}
	err      error
	original *term.State
}

func openTerminal(t *testing.T, env []string, cols, rows uint16) *session {
	t.Helper()
	cmd := exec.Command(executable)
	cmd.Env = env
	terminal, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	t.Cleanup(func() { terminal.Close() })
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &session{cmd: cmd, terminal: terminal, done: make(chan struct{}), original: original}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			s.mu.Lock()
			s.output.Write(buffer[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		s.err = cmd.Wait()
		// A Darwin PTY may not report EOF until the master is closed.
		select {
		case <-readDone:
		case <-time.After(100 * time.Millisecond):
		}
		close(s.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = terminal.Close()
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			t.Error("process did not reap")
		}
	})
	return s
}

func (s *session) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

// mark records the current output length; textFrom returns only output that
// arrived after the mark, for asserting what a later render shows.
func (s *session) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.Len()
}

func (s *session) textFrom(mark int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.output.Bytes()[mark:])
}

func (s *session) expect(t *testing.T, fragments ...string) {
	t.Helper()
	s.expectFrom(t, 0, fragments...)
}

func (s *session) expectFrom(t *testing.T, mark int, fragments ...string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		text := s.textFrom(mark)
		found := true
		for _, fragment := range fragments {
			found = found && strings.Contains(text, fragment)
		}
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal did not show %q; output: %q", fragments, s.textFrom(mark))
}

func (s *session) input(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(s.terminal, text); err != nil {
		t.Fatal(err)
	}
}

func (s *session) exit(t *testing.T, success bool) {
	t.Helper()
	select {
	case <-s.done:
		if (s.err == nil) != success {
			t.Fatalf("exit: %v; output: %q", s.err, s.text())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("executable did not exit within 3s; output: %q", s.text())
	}
}

// fakeAPI is a stand-in Kubernetes pod API for offline executable tests: it
// serves per-name pod gets (200, 404, 403, 500) and deletes, records
// requests, and requires the bearer token when one is configured.
type fakeAPI struct {
	mu        sync.Mutex
	pods      map[string]any // *podDoc or error status
	deleted   []string
	gets      int
	token     string
	namespace string
	server    *httptest.Server
}

type podDoc struct {
	name           string
	phase          string
	startedAgo     time.Duration
	terminating    bool
	restarts       int32
	image          string
	runtimeClass   string
	requestsCPU    string
	requestsMemory string
	limitsCPU      string
	limitsMemory   string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{pods: map[string]any{}, namespace: "containerssh-sessions"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.token != "" && r.Header.Get("Authorization") != "Bearer "+api.token {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", "credentials rejected")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[0] != api.namespace || parts[1] != "pods" {
			http.NotFound(w, r)
			return
		}
		name := parts[2]
		switch r.Method {
		case http.MethodGet:
			api.gets++
			switch doc := api.pods[name].(type) {
			case *podDoc:
				writePod(w, doc)
			case *statusDoc:
				writeStatus(w, doc.code, doc.reason, doc.message)
			default:
				writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("pods %q not found", name))
			}
		case http.MethodDelete:
			api.deleted = append(api.deleted, name)
			switch doc := api.pods[name].(type) {
			case *podDoc:
				doc.terminating = true
				writeStatus(w, http.StatusOK, "", "")
			case *statusDoc:
				writeStatus(w, doc.code, doc.reason, doc.message)
			default:
				writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("pods %q not found", name))
			}
		default:
			writeStatus(w, http.StatusMethodNotAllowed, "NotAllowed", "method not allowed")
		}
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

type statusDoc struct {
	code    int
	reason  string
	message string
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": code}
	if reason != "" {
		body["reason"] = reason
	}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writePod(w http.ResponseWriter, doc *podDoc) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	start := time.Now().Add(-doc.startedAgo).UTC().Format(time.RFC3339)
	fmt.Fprintf(w, `{
		"metadata": {"name": %q%s},
		"spec": {"runtimeClassName": %q, "containers": [{"name": "shell", "image": %q,
			"resources": {"requests": {"cpu": %q, "memory": %q}, "limits": {"cpu": %q, "memory": %q}}}]},
		"status": {"phase": %q, "startTime": %q, "containerStatuses": [{"restartCount": %d}]}
	}`, doc.name, terminationJSON(doc.terminating), doc.runtimeClass, doc.image,
		doc.requestsCPU, doc.requestsMemory, doc.limitsCPU, doc.limitsMemory, doc.phase, start, doc.restarts)
}

func terminationJSON(terminating bool) string {
	if !terminating {
		return ""
	}
	return fmt.Sprintf(`, "deletionTimestamp": %q`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339))
}

// setToken configures the bearer token the API requires, race-safely.
func (a *fakeAPI) setToken(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = token
}

// envFor returns the executable environment pointed at the fake API.
func (a *fakeAPI) envFor(owner, templates string, extra ...string) []string {
	a.mu.Lock()
	token, base := a.token, a.server.URL
	a.mu.Unlock()
	return append(environment(owner, extra...),
		"DEV_BOX_KUBE_API_URL="+base,
		"DEV_BOX_KUBE_API_TOKEN="+token,
		"DEV_BOX_NAMESPACE=containerssh-sessions",
		"DEV_BOX_BOX_TEMPLATES="+templates,
	)
}

// boxName mirrors the deterministic names the TUI derives.
func boxName(owner, template string) string {
	return "box-" + shortHash(owner+"\x00"+template)
}

func shortHash(value string) string {
	// Keep in sync with the config server contract; the golden test in the
	// model tests pins this against the live cluster.
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}

func TestCommandsAndEscapeShortcutsCannotLeaveTheApplication(t *testing.T) {
	s := openTerminal(t, environment("alice.example"), 80, 24)
	s.expect(t, "q: quit")
	// Ctrl-Z, Esc, Ctrl-D, "ESC q" as a literal sequence, and a typed shell
	// command must neither exit the application nor execute anything.
	for _, input := range []string{"\x1a", "\x1b", "\x04", "\x1bq", "\r/bin/sh\rprintf 'DEV_BOX_%s' ESCAPE_EXECUTED\r"} {
		s.input(t, input)
		select {
		case <-s.done:
			t.Fatalf("input %q exited the application", input)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if strings.Contains(s.text(), "DEV_BOX_ESCAPE_EXECUTED") {
		t.Fatal("typed command was executed")
	}
	s.input(t, "q")
	s.exit(t, true) // also proves Ctrl-Z did not suspend the process
	restored, err := term.GetState(int(s.terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if *restored != *s.original {
		t.Fatal("q did not restore terminal settings")
	}
}

func TestCtrlCQuitsFromTheListViewOnly(t *testing.T) {
	api := newFakeAPI(t)
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: 2 * time.Hour,
		restarts: 1, image: "containerssh/containerssh-guest-image",
		runtimeClass: "kata-qemu-runtime-rs", limitsCPU: "500m", limitsMemory: "1Gi"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu", "NO_COLOR=1"), 120, 24)
	s.expect(t, "Running")
	s.input(t, "d")
	s.expect(t, "Delete box")
	s.input(t, "\x03") // inside a dialog, Ctrl+C only cancels
	select {
	case <-s.done:
		t.Fatal("Ctrl+C quit from inside the dialog")
	case <-time.After(300 * time.Millisecond):
	}
	mark := s.mark()
	s.input(t, "\x03") // from the list view, Ctrl+C quits
	s.exit(t, true)
	_ = mark
}

func TestOwnerIsTextNotTerminalControl(t *testing.T) {
	owner := "Björk.李\x1b]52;c;clipboard\a\x1b[31m\r\nINJECTED\u009b32m\u202ebidi\u2028line"
	s := openTerminal(t, environment(owner, "NO_COLOR=1"), 160, 24)
	s.expect(t, "Owner: Björk.李", "q: quit")
	for _, control := range []string{"\x1b]52;c;clipboard", "\a", "\x1b[31m", "\r\nINJECTED", "\x9b", "\u202e", "\u2028"} {
		if strings.Contains(s.text(), control) {
			t.Errorf("owner injected control sequence %q into terminal", control)
		}
	}
	s.input(t, "q")
	s.exit(t, true)
}

func TestSmallTerminalKeepsQuitVisibleAndResizes(t *testing.T) {
	s := openTerminal(t, environment("alice.with.a.long.verified.owner.name", "NO_COLOR=1"), 24, 5)
	s.expect(t, "settings", "Owner: alice", "No box templates", "q: quit")
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 100, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "dev.box settings", "Owner: alice.with.a.long.verified.owner.name", "No box templates", "q: quit")
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 20, Rows: 5}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "No box templates", "q: quit")
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 12, Rows: 3}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "settings", "q: quit")
	s.input(t, "q")
	s.exit(t, true)
}

func TestWithoutPTYExitsWithHelpfulError(t *testing.T) {
	for _, streams := range []string{"neither", "stdin-only", "stdout-only"} {
		t.Run(streams, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable)
			cmd.Env = environment("alice.example")
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if streams == "stdin-only" {
				cmd.Stdin = slave
			}
			if streams == "stdout-only" {
				cmd.Stdout = slave
			}
			err = cmd.Run()
			if ctx.Err() != nil {
				t.Fatal("non-PTY invocation hung")
			}
			if err == nil {
				t.Fatal("non-PTY invocation succeeded")
			}
			if !strings.Contains(output.String(), "PTY") || !strings.Contains(output.String(), "ssh -t") {
				t.Fatalf("unhelpful non-PTY failure: %q", output.String())
			}
			if strings.Contains(output.String(), "q: quit") {
				t.Fatal("non-PTY opened UI")
			}
		})
	}
}

func TestTerminalWaitsForInitialResize(t *testing.T) {
	s := openTerminal(t, environment("alice.example", "NO_COLOR=1"), 0, 0)
	// ContainerSSH learns the agent PID before it sends the initial resize.
	time.Sleep(200 * time.Millisecond)
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "dev.box settings", "Owner: alice.example", "q: quit")
	s.input(t, "q")
	s.exit(t, true)
}

func TestZeroSizedTerminalFailsHelpfully(t *testing.T) {
	s := openTerminal(t, environment("alice.example"), 0, 0)
	s.exit(t, false)
	s.expect(t, "PTY", "ssh -t")
}

func TestNoOwnerFallbackFromClientOrImageIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	for _, env := range environment("") {
		if !strings.HasPrefix(env, "DEV_BOX_AUTHENTICATED_OWNER=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "USER=settings", "LOGNAME=settings", "SSH_ORIGINAL_COMMAND=alice.example")
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("expected bounded identity failure, got %v", err)
	}
	if !strings.Contains(string(output), "DEV_BOX_AUTHENTICATED_OWNER") || !strings.Contains(string(output), "missing") {
		t.Fatalf("identity was not rejected: %q", output)
	}
}

func TestSimultaneousTerminalsDisplayOnlyTheirOwnOwners(t *testing.T) {
	alice := openTerminal(t, environment("alice.example", "NO_COLOR=1"), 80, 24)
	bob := openTerminal(t, environment("bob.example", "NO_COLOR=1"), 80, 24)
	alice.expect(t, "Owner: alice.example", "q: quit")
	bob.expect(t, "Owner: bob.example", "q: quit")
	if strings.Contains(alice.text(), "bob.example") || strings.Contains(bob.text(), "alice.example") {
		t.Fatal("owner leaked across terminal sessions")
	}
	alice.input(t, "q")
	bob.input(t, "q")
	alice.exit(t, true)
	bob.exit(t, true)
}

func TestMissingTrustedOwnerFailsClosed(t *testing.T) {
	for _, owner := range []string{"", " \t\r\n"} {
		t.Run(fmt.Sprintf("owner=%q", owner), func(t *testing.T) {
			s := openTerminal(t, environment(owner), 80, 24)
			s.exit(t, false)
			s.expect(t, "DEV_BOX_AUTHENTICATED_OWNER", "missing")
			if strings.Contains(s.text(), "q: quit") {
				t.Fatal("missing trusted identity opened the UI")
			}
		})
	}
}

// A malformed trusted template list fails closed before the UI opens.
func TestMalformedTrustedTemplateListFailsClosed(t *testing.T) {
	for _, templates := range []string{"bad name", "a/b", "ok;rm -rf"} {
		t.Run(templates, func(t *testing.T) {
			s := openTerminal(t, environment("alice.example", "DEV_BOX_BOX_TEMPLATES="+templates), 80, 24)
			s.exit(t, false)
			s.expect(t, "DEV_BOX_BOX_TEMPLATES", "invalid template name")
			if strings.Contains(s.text(), "q: quit") {
				t.Fatal("malformed template list opened the UI")
			}
		})
	}
}

// Empty trusted template list is valid: the UI shows the empty state.
func TestEmptyTemplateListRendersEmptyState(t *testing.T) {
	s := openTerminal(t, environment("alice.example", "NO_COLOR=1", "DEV_BOX_BOX_TEMPLATES="), 100, 24)
	s.expect(t, "No box templates are available.", "q: quit")
	s.input(t, "d")
	s.expect(t, "nothing to delete")
	s.input(t, "q")
	s.exit(t, true)
}

func TestTerminalShowsSettingsAndQExits(t *testing.T) {
	s := openTerminal(t, environment("alice.example", "NO_COLOR=1"), 80, 24)
	s.expect(t, "dev.box settings", "Owner: alice.example", "Connection:", "q: quit")
	s.input(t, "q")
	s.exit(t, true)
}

// The management flow against a stand-in Kubernetes API: rows from gets,
// not-created rows, selection, delete confirm/cancel, refresh, Terminating
// transition, recreation hint — the real executable, real HTTP client, and
// real rendering, offline.
func TestManagementListSelectDeleteAndRecreate(t *testing.T) {
	api := newFakeAPI(t)
	api.setToken("test-token")
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: 2 * time.Hour,
		restarts: 1, image: "containerssh/containerssh-guest-image",
		runtimeClass: "kata-qemu-runtime-rs",
		requestsCPU:  "250m", requestsMemory: "512Mi", limitsCPU: "500m", limitsMemory: "1Gi"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu,data-engine", "NO_COLOR=1", "DEV_BOX_MAX_BOXES=2"), 140, 24)

	// The sorted master list starts at data-engine (not created); its detail
	// pane names the create command, and the master pane foots the box cap.
	s.expect(t,
		"Owner: alice.example · Live",
		"data-engine",
		"not created",
		"ubuntu",
		"Running",
		"ssh data-engine@<host> creates this box",
		"created 1/2",
	)
	// Delete is never offered for a box that does not exist: the initially
	// selected first row (alphabetical) is the not-created data-engine.
	s.input(t, "d")
	s.expect(t, "data-engine is not created; log in as data-engine@<host> to create it")

	// Selecting ubuntu swaps the detail pane to its status strip and ledger.
	s.input(t, "j")
	s.expect(t,
		"up 2h",
		"RESTARTS",
		"containerssh/containerssh-guest-image",
		"kata-qemu-runtime-rs",
		"REQUESTS",
		"512Mi/250m",
		"LIMITS",
		"1Gi/500m",
	)

	// j/k and arrows move the selection; the delete popup always names the
	// selected box, which is visible proof of the selection.
	s.input(t, "d")
	s.expect(t, "Delete box \"ubuntu\" ("+ubuntu+")?")
	mark := s.mark()
	s.input(t, "n")
	s.input(t, "k")
	s.input(t, "d")
	// Observe the intermediate list frame before reopening an identical popup;
	// otherwise the differential renderer can legitimately emit no new bytes.
	s.expectFrom(t, mark, "data-engine is not created")
	s.input(t, "\x1b[B") // down arrow: back to ubuntu
	// Wait for this popup, not an earlier identical title in PTY history.
	mark = s.mark()
	s.input(t, "d")
	s.expectFrom(t, mark, "Delete box \"ubuntu\" ("+ubuntu+")?")
	s.input(t, "\x1b") // Esc cancels the dialog
	mark = s.mark()
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(s.textFrom(mark), "Delete box") {
		t.Fatal("Esc left the confirmation dialog open")
	}

	// r forces a refresh: new gets must arrive.
	api.mu.Lock()
	before := api.gets
	api.mu.Unlock()
	s.input(t, "r")
	waitFor(t, time.Second, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return api.gets >= before+2
	})

	// Cancel keeps the box; the popup lists all three consequences.
	mark = s.mark()
	s.input(t, "d")
	s.expectFrom(t, mark, "Delete box \"ubuntu\" ("+ubuntu+")?",
		"Local disk data is destroyed; data on retained PVCs is kept.",
		"active sessions in the box are killed",
		"a fresh box is created on your next login:",
		"ssh ubuntu@<host>")
	s.input(t, "n")
	mark = s.mark()
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(s.textFrom(mark), "Delete box") {
		t.Fatal("cancel left the confirmation dialog open")
	}
	api.mu.Lock()
	if len(api.deleted) != 0 {
		t.Fatal("cancel deleted the box")
	}
	api.mu.Unlock()

	// Confirm deletes through the API and shows Terminating, then the
	// not-created row once the pod is gone.
	mark = s.mark()
	s.input(t, "d")
	s.expectFrom(t, mark, "Delete box")
	s.input(t, "y")
	s.expect(t, "Terminating")
	api.mu.Lock()
	if len(api.deleted) != 1 || api.deleted[0] != ubuntu {
		t.Fatalf("deletes = %v", api.deleted)
	}
	delete(api.pods, ubuntu)
	api.mu.Unlock()
	mark = s.mark()
	// The differential renderer may repaint only changed cells, so assert the
	// redrawn count, not the contiguous "created 0/2" line.
	s.expectFrom(t, mark, "ssh ubuntu@<host> creates this box", "0/2")
	s.input(t, "q")
	s.exit(t, true)
}

func TestManagementUnreachableDisablesDelete(t *testing.T) {
	// A URL nobody answers: the connection degrades honestly, delete is
	// refused, and the UI stays usable.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	_ = closed.Close()
	s := openTerminal(t, environment("alice.example", "NO_COLOR=1",
		"DEV_BOX_KUBE_API_URL=http://"+address,
		"DEV_BOX_KUBE_NAMESPACE=containerssh-sessions",
		"DEV_BOX_BOX_TEMPLATES=ubuntu"), 120, 24)
	s.expect(t, "Unreachable", "unreachable", "q: quit")
	s.input(t, "d")
	s.expect(t, "delete is unavailable")
	s.input(t, "y") // never confirmable without a dialog
	s.input(t, "q")
	s.exit(t, true)
}

func TestManagementPartialAndForbiddenRows(t *testing.T) {
	api := newFakeAPI(t)
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &statusDoc{code: http.StatusForbidden, reason: "Forbidden", message: "grants changed"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu,data-engine", "NO_COLOR=1"), 130, 24)
	s.expect(t, "Partial", "ubuntu", "reconnect")
	// The selected forbidden row explains how to recover: reconnect settings.
	s.input(t, "j") // the sorted list puts ubuntu second
	s.expect(t, "authorization changed", "reconnect settings to refresh")
	s.input(t, "q")
	s.exit(t, true)
}

func TestManagementDumbTerminalRendersPlainText(t *testing.T) {
	api := newFakeAPI(t)
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: time.Hour,
		restarts: 0, image: "guest-image", runtimeClass: "kata-qemu-runtime-rs",
		limitsCPU: "1", limitsMemory: "2Gi"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu", "TERM=dumb"), 120, 24)
	s.expect(t, "Running", "Owner: alice.example")
	for _, sgr := range []string{"\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[2m", "\x1b[1m"} {
		if strings.Contains(s.text(), sgr) {
			t.Fatalf("dumb terminal received color sequence %q", sgr)
		}
	}
	s.input(t, "q")
	s.exit(t, true)
}

func TestManagementRejectsMissingToken(t *testing.T) {
	api := newFakeAPI(t)
	api.setToken("secret")
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: time.Hour,
		restarts: 0, image: "guest-image", runtimeClass: "kata-qemu-runtime-rs",
		limitsCPU: "1", limitsMemory: "2Gi"}
	env := environment("alice.example", "NO_COLOR=1",
		"DEV_BOX_KUBE_API_URL="+api.server.URL,
		"DEV_BOX_NAMESPACE=containerssh-sessions",
		"DEV_BOX_BOX_TEMPLATES=ubuntu") // no DEV_BOX_KUBE_API_TOKEN
	s := openTerminal(t, env, 120, 24)
	s.expect(t, "Unreachable")
	s.input(t, "q")
	s.exit(t, true)
}

// The trusted cap env renders the created N/max line and fails closed when
// malformed.
func TestMaxBoxesEnvRendersCapAndFailsClosed(t *testing.T) {
	api := newFakeAPI(t)
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: time.Hour,
		restarts: 0, image: "guest-image", runtimeClass: "kata-qemu-runtime-rs",
		requestsCPU: "250m", requestsMemory: "512Mi", limitsCPU: "500m", limitsMemory: "1Gi"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu,data-engine", "NO_COLOR=1",
		"DEV_BOX_MAX_BOXES=2"), 120, 24)
	s.expect(t, "created 1/2")
	// The selected box's detail pane carries its requests.
	s.input(t, "j")
	s.expect(t, "REQUESTS", "512Mi/250m")
	s.input(t, "q")
	s.exit(t, true)

	// A malformed trusted cap fails closed before the UI opens.
	bad := openTerminal(t, environment("alice.example", "DEV_BOX_BOX_TEMPLATES=ubuntu",
		"DEV_BOX_MAX_BOXES=three"), 80, 24)
	bad.exit(t, false)
	bad.expect(t, "DEV_BOX_MAX_BOXES")
	if strings.Contains(bad.text(), "q: quit") {
		t.Fatal("malformed trusted cap opened the UI")
	}
}

func TestManagementColorsAreUsedOnColorTerminals(t *testing.T) {
	api := newFakeAPI(t)
	ubuntu := boxName("alice.example", "ubuntu")
	api.pods[ubuntu] = &podDoc{name: ubuntu, phase: "Running", startedAgo: time.Hour,
		restarts: 0, image: "guest-image", runtimeClass: "kata-qemu-runtime-rs",
		limitsCPU: "1", limitsMemory: "2Gi"}
	s := openTerminal(t, api.envFor("alice.example", "ubuntu"), 120, 24)
	s.expect(t, "Running")
	if !strings.Contains(s.text(), "\x1b[32m") {
		t.Fatal("a color terminal did not receive the green live/running tint")
	}
	s.input(t, "q")
	s.exit(t, true)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
