//go:build unix

package main_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

func environment(owner string) []string {
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DEV_BOX_AUTHENTICATED_OWNER=") && !strings.HasPrefix(value, "TERM=") {
			env = append(env, value)
		}
	}
	return append(env, "TERM=xterm-256color", "DEV_BOX_AUTHENTICATED_OWNER="+owner)
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

func openTerminal(t *testing.T, owner string, cols, rows uint16) *session {
	t.Helper()
	cmd := exec.Command(executable)
	cmd.Env = environment(owner)
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

func (s *session) expect(t *testing.T, fragments ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		text := s.text()
		found := true
		for _, fragment := range fragments {
			found = found && strings.Contains(text, fragment)
		}
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal did not show %q; output: %q", fragments, s.text())
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

func TestCommandsAndEscapeShortcutsCannotLeaveTheApplication(t *testing.T) {
	s := openTerminal(t, "alice.example", 80, 24)
	s.expect(t, "q: quit")
	for _, input := range []string{"\x1a", "\x1b", "\x03\x04", "\x1bq", "\r/bin/sh\rprintf 'DEV_BOX_%s' ESCAPE_EXECUTED\r"} {
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

func TestOwnerIsTextNotTerminalControl(t *testing.T) {
	owner := "Björk.李\x1b]52;c;clipboard\a\x1b[31m\r\nINJECTED\u009b32m\u202ebidi\u2028line"
	s := openTerminal(t, owner, 160, 24)
	s.expect(t, "Owner: Björk.李", "q: quit")
	for _, control := range []string{"\x1b]52;c;clipboard", "\a", "\x1b[31m", "\r\nINJECTED", "\u009b", "\u202e", "\u2028"} {
		if strings.Contains(s.text(), control) {
			t.Errorf("owner injected control sequence %q into terminal", control)
		}
	}
	s.input(t, "q")
	s.exit(t, true)
}

func TestSmallTerminalKeepsQuitVisibleAndResizes(t *testing.T) {
	s := openTerminal(t, "alice.with.a.long.verified.owner.name", 24, 5)
	s.expect(t, "Settings", "POC", "read-only", "Owner: alice", "q: quit")
	// Observe subsequent output, not the earlier large/small screen.
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 100, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "Owner: alice.with.a.long.verified.owner.name", "q: quit")
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 20, Rows: 5}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "read-only", "q: quit")
	s.mu.Lock()
	s.output.Reset()
	s.mu.Unlock()
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 12, Rows: 3}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "Settings", "POC/RO", "q: quit")
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
	s := openTerminal(t, "alice.example", 0, 0)
	// ContainerSSH learns the agent PID before it sends the initial resize.
	time.Sleep(200 * time.Millisecond)
	if err := pty.Setsize(s.terminal, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	s.expect(t, "Settings", "Owner: alice.example", "q: quit")
	s.input(t, "q")
	s.exit(t, true)
}

func TestZeroSizedTerminalFailsHelpfully(t *testing.T) {
	s := openTerminal(t, "alice.example", 0, 0)
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
	alice := openTerminal(t, "alice.example", 80, 24)
	bob := openTerminal(t, "bob.example", 80, 24)
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
			s := openTerminal(t, owner, 80, 24)
			s.exit(t, false)
			s.expect(t, "DEV_BOX_AUTHENTICATED_OWNER", "missing")
			if strings.Contains(s.text(), "q: quit") {
				t.Fatal("missing trusted identity opened the UI")
			}
		})
	}
}

func TestTerminalShowsReadOnlySettingsAndQExits(t *testing.T) {
	s := openTerminal(t, "alice.example", 80, 24)
	s.expect(t, "Settings", "POC", "read-only", "Owner: alice.example", "q: quit")
	s.input(t, "q")
	s.exit(t, true)
}
