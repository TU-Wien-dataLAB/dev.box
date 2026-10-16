// settings-acceptance-helper is a disposable acceptance fixture, not a management API.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxBody = 1 << 20
const tuiPath = "/usr/local/bin/dev-box-settings"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected kill-tui or config-proxy")
	}
	switch args[0] {
	case "kill-tui":
		if len(args) != 1 {
			return fmt.Errorf("kill-tui accepts no arguments")
		}
		return killTUI()
	case "config-proxy":
		flags := flag.NewFlagSet("config-proxy", flag.ContinueOnError)
		flags.SetOutput(io.Discard) // Never echo supplied URLs or credentials on errors.
		upstream := flags.String("upstream", "http://containerssh-config-server:8080", "fixed config upstream")
		listen := flags.String("listen", ":8080", "listen address")
		owner := flags.String("owner", "", "canonical owner")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
			return fmt.Errorf("invalid config-proxy arguments")
		}
		handler, err := newProxy(*upstream, *owner)
		if err != nil {
			return err
		}
		server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: nil}
		// The fixture has no request logging or administrative endpoints.
		if server.ListenAndServe() != nil {
			return fmt.Errorf("config-proxy stopped")
		}
		return nil
	default:
		return fmt.Errorf("expected kill-tui or config-proxy")
	}
}

func killTUI() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("kill-tui requires Linux /proc")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("cannot inspect processes")
	}
	var candidates []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // Process exited during enumeration.
		if err != nil {
			return fmt.Errorf("cannot inspect processes")
		} // Fail closed on hidden processes.
		if exe == tuiPath {
			candidates = append(candidates, pid)
		}
	}
	if len(candidates) != 1 {
		return fmt.Errorf("expected exactly one Settings process")
	}
	pid := candidates[0]
	if pid <= 1 {
		return fmt.Errorf("refusing protected PID")
	}
	// FindProcess uses a pidfd on supported Linux kernels. Recheck identity after
	// opening the handle, so a PID recycled during enumeration is not trusted.
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("cannot open Settings process")
	}
	defer process.Release()
	base := filepath.Join("/proc", strconv.Itoa(pid))
	exe, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || exe != tuiPath {
		return fmt.Errorf("Settings process changed")
	}
	status, err := os.ReadFile(filepath.Join(base, "status"))
	if err != nil {
		return fmt.Errorf("cannot inspect Settings UID")
	}
	sameUID := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			ids := strings.Fields(line)
			// Require all real/effective/saved/filesystem UIDs to match, not just one.
			if len(ids) == 5 {
				sameUID = true
				for _, value := range ids[1:] {
					uid, err := strconv.Atoi(value)
					if err != nil || uid != os.Geteuid() {
						sameUID = false
					}
				}
			}
		}
	}
	if !sameUID {
		return fmt.Errorf("Settings process UID mismatch")
	}
	if err := process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("cannot kill Settings process")
	}
	fmt.Printf("PID=%d success=true\n", pid)
	return nil
}

func newProxy(upstream, owner string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil || target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil || target.RawQuery != "" || target.ForceQuery || strings.Contains(upstream, "#") || (target.Path != "" && target.Path != "/") || target.Opaque != "" || owner == "" {
		return nil, fmt.Errorf("invalid upstream or owner")
	}
	target.Path = "/config"
	// No environment proxy, redirects, automatic decompression, or retryable requests.
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableCompression: true, MaxResponseHeaderBytes: 16 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "invalid request", 400)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				http.Error(w, "request too large", 413)
			} else {
				http.Error(w, "invalid request", 400)
			}
			return
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var username, authenticated string
		for key, dest := range map[string]*string{"username": &username, "authenticatedUsername": &authenticated} {
			if value, ok := fields[key]; ok {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, dest) != nil {
					http.Error(w, "invalid request", 400)
					return
				}
			}
		}
		if username == "settings" && authenticated == owner {
			delete(fields, "authenticatedUsername")
			body, _ = json.Marshal(fields)
		}
		request, _ := http.NewRequestWithContext(r.Context(), "POST", target.String(), bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		defer response.Body.Close()
		result, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
		if err != nil || len(result) > maxBody {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(result)
	}), nil
}
