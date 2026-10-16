package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

var helperOnce sync.Once
var helperBinary string
var helperBuildError error

func executable(t *testing.T) string {
	t.Helper()
	helperOnce.Do(func() {
		var dir string
		dir, helperBuildError = os.MkdirTemp("", "settings-helper-test-")
		if helperBuildError != nil {
			return
		}
		helperBinary = filepath.Join(dir, "helper")
		output, err := exec.Command("go", "build", "-o", helperBinary, ".").CombinedOutput()
		if err != nil {
			helperBuildError = fmt.Errorf("build: %w: %s", err, output)
		}
	})
	if helperBuildError != nil {
		t.Fatal(helperBuildError)
	}
	return helperBinary
}

func TestMain(m *testing.M) {
	code := m.Run()
	if helperBinary != "" {
		_ = os.RemoveAll(filepath.Dir(helperBinary))
	}
	os.Exit(code)
}

func TestExecutableRejectsUnsafeInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"kill-tui", "extra"}, {"config-proxy", "--owner", ""}, {"config-proxy", "--owner", "alice", "--upstream", "http://user:secret@example.com"}} {
		output, err := exec.Command(executable(t), args...).CombinedOutput()
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		if bytes.Contains(output, []byte("secret")) {
			t.Fatal("leaked upstream credentials")
		}
	}
	if runtime.GOOS != "linux" {
		output, err := exec.Command(executable(t), "kill-tui").CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("Linux")) {
			t.Fatalf("non-Linux result: %s %v", output, err)
		}
	}
}

// Opt in ONLY in a disposable Linux environment with writable /usr/local/bin.
// Refuse to replace an existing TUI. All victims are real child executables.
func TestKillTUIRealProcesses(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("SETTINGS_FIXTURE_DISPOSABLE_LINUX") != "1" {
		t.Skip("requires explicit disposable Linux opt-in")
	}
	const target = "/usr/local/bin/dev-box-settings"
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("target already exists or cannot be checked; refusing")
	}
	source := filepath.Join(t.TempDir(), "sleeper.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main(){time.Sleep(time.Hour)}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "dev-box-settings")
	if output, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("sleeper: %s %v", output, err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(target)
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	start := func(path string, foreign bool) *exec.Cmd {
		t.Helper()
		cmd := exec.Command(path)
		if foreign {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	run := func(success bool) []byte {
		t.Helper()
		output, err := exec.Command(executable(t), "kill-tui").CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("kill-tui: %s %v", output, err)
		}
		return output
	}
	run(false) // zero exact targets
	lookalike := start(binary, false)
	run(false) // same basename is insufficient
	if err := lookalike.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("lookalike was killed")
	}
	one := start(target, false)
	two := start(target, false)
	run(false) // ambiguity must never signal either process
	for _, cmd := range []*exec.Cmd{one, two} {
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatal("ambiguous target killed")
		}
	}
	_ = two.Process.Kill()
	_ = two.Wait()
	output := run(true)
	if !bytes.Contains(output, []byte("PID="+strconv.Itoa(one.Process.Pid)+" success=true")) {
		t.Fatalf("missing success and PID: %s", output)
	}
	err = one.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("target was not killed: %v", err)
	}
	status := exit.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("wrong exit status: %v", status)
	}
	if os.Geteuid() == 0 {
		foreign := start(target, true)
		run(false)
		if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatal("foreign UID killed")
		}
	}
}

func TestProxyRejectsUnsafeRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("rejected request reached upstream") }))
	defer upstream.Close()
	handler, err := newProxy(upstream.URL, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/config", `{}`, 405}, {"POST", "/other", `{}`, 404},
		{"POST", "/config?url=http://evil", `{}`, 400},
		{"POST", "/config", `{`, 400}, {"POST", "/config", `null`, 400},
		{"POST", "/config", `[]`, 400}, {"POST", "/config", `{} {}`, 400},
		{"POST", "/config", `{"username":42}`, 400},
		{"POST", "/config", `{"authenticatedUsername":[]}`, 400},
		{"POST", "/config", strings.Repeat("x", (1<<20)+1), 413},
	} {
		t.Run(tc.method+tc.path+tc.body[:min(len(tc.body), 20)], func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status {
				t.Fatalf("got %d, want %d", response.Code, tc.status)
			}
		})
	}
}

func TestProxyBoundsAndSanitizesUpstreamFailures(t *testing.T) {
	for _, mode := range []string{"disconnect", "oversize", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect") }))
			defer destination.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "disconnect":
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat("s", (1<<20)+1)))
				case "redirect":
					http.Redirect(w, r, destination.URL, 307)
				}
			}))
			handler, err := newProxy(upstream.URL, "alice")
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/config", strings.NewReader(`{}`)))
			upstream.Close()
			if calls != 1 {
				t.Fatalf("upstream calls: %d", calls)
			}
			if mode == "redirect" {
				if response.Code != 307 {
					t.Fatalf("redirect status: %d", response.Code)
				}
				return
			}
			if response.Code != 502 || response.Body.String() != "upstream unavailable\n" {
				t.Fatalf("unsanitized failure: %d body length %d", response.Code, response.Body.Len())
			}
		})
	}
}

func TestProxyValidatesStartup(t *testing.T) {
	for _, upstream := range []string{"", "/config", "ftp://example.com", "http://user:password@example.com", "http://example.com?secret=x", "http://example.com#x", "http://example.com#", "http://example.com?", "http://example.com/other"} {
		if _, err := newProxy(upstream, "alice"); err == nil {
			t.Errorf("accepted unsafe upstream %q", upstream)
		}
	}
	if _, err := newProxy("http://localhost:8080", ""); err == nil {
		t.Error("accepted empty owner")
	}
}

func TestProxyRemovesOnlySelectedOwner(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"selected", `{"username":"settings","authenticatedUsername":"alice","ip":"test"}`, `{"ip":"test","username":"settings"}`},
		{"other owner", `{"username":"settings","authenticatedUsername":"bob"}`, `{"username":"settings","authenticatedUsername":"bob"}`},
		{"other template", `{"username":"ubuntu","authenticatedUsername":"alice"}`, `{"username":"ubuntu","authenticatedUsername":"alice"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.RequestURI() != "/config" || string(b) != tc.want {
					t.Errorf("unexpected upstream request: %s %s %s", r.Method, r.URL, b)
				}
				w.WriteHeader(418)
				_, _ = w.Write([]byte(`{"error":"denied"}`))
			}))
			defer upstream.Close()
			handler, err := newProxy(upstream.URL, "alice")
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/config", strings.NewReader(tc.body)))
			if response.Code != 418 || response.Body.String() != `{"error":"denied"}` {
				t.Fatalf("response: %d %s", response.Code, response.Body)
			}
		})
	}
}
