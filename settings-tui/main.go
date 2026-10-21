// Command dev-box-settings is the code-owned dev.box management application.
//
// It runs as the only program of the settings guest image and is launched
// directly by ContainerSSH on the PTY of an `ssh -t settings@host` session.
// It renders one row per box template the operator published for the verified
// owner, refreshed from the Kubernetes API through the owner's projected
// token, and offers exactly one management action: delete (confirm with y).
//
// Trusted inputs (set by the config server, never by the client):
//
//	DEV_BOX_AUTHENTICATED_OWNER  the server-verified identity
//	DEV_BOX_BOX_TEMPLATES        comma-separated persistent box template names
//	DEV_BOX_MAX_BOXES            per-owner box cap for the created N/max line
//	                              (0/unset hides the element; the count itself
//	                              is advisory — the config server is
//	                              authoritative)
//
// Failing closed means exiting with an explanatory message: there is no shell
// in the image to fall back to.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

func run() error {
	owner := os.Getenv(ownerEnv)
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("trusted %s is missing; contact the operator", ownerEnv)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("a usable PTY is required on stdin and stdout; connect with ssh -t settings@HOST")
	}
	// The gateway sends initial dimensions after the guest agent reports its
	// PID. A newly allocated PTY may briefly be 0x0 even for a valid client.
	deadline := time.Now().Add(2 * time.Second)
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	for err == nil && (width < 1 || height < 1) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		width, height, err = term.GetSize(int(os.Stdout.Fd()))
	}
	if err != nil || width < 1 || height < 1 {
		return fmt.Errorf("a usable PTY with nonzero dimensions is required; connect with ssh -t settings@HOST")
	}
	// Identity is trusted for authentication, not for terminal interpretation.
	// Keep the canonical (trimmed) raw identity for box naming — the config
	// server hashes exactly this value — and let the view sanitize it for
	// display: controls, bidi/format characters, and non-printing separators
	// never reach a renderer, and the identity is never parsed as ANSI markup.
	owner = strings.TrimSpace(owner)
	templates, err := parseTemplates(os.Getenv(templatesEnv))
	if err != nil {
		return err
	}
	maxBoxes, err := parseMaxBoxes(os.Getenv(maxBoxesEnv))
	if err != nil {
		return err
	}
	// A missing client is a degraded deployment, not a crash: the UI stays up,
	// reports Unreachable, dims its rows, and disables delete.
	client, err := newBoxClient()
	if err != nil {
		client = unavailableClient{err: err}
	}
	_, err = tea.NewProgram(newModel(owner, templates, maxBoxes, client, width, height),
		tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Settings:", err)
		os.Exit(1)
	}
}
