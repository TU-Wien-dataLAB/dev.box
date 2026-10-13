package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

type settings struct {
	owner         string
	width, height int
}

func (s settings) Init() tea.Cmd { return nil }

func (s settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.Code == 'q' && msg.Mod == 0 {
			return s, tea.Quit
		}
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	}
	return s, nil
}

func (s settings) View() tea.View {
	lines := []string{"Settings", "POC / read-only", "Owner: " + s.owner, "q: quit"}
	if s.height == 3 {
		lines = []string{"Settings", "POC/RO " + s.owner, "q: quit"}
	} else if s.height == 2 {
		lines = []string{"Settings POC/RO", "q: quit"}
	} else if s.height < 2 {
		lines = []string{"q: quit"}
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, s.width), "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

func run() error {
	owner := os.Getenv("DEV_BOX_AUTHENTICATED_OWNER")
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("trusted DEV_BOX_AUTHENTICATED_OWNER is missing; contact the operator")
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
	// Replace controls, bidi/format characters, and non-printing separators
	// before any renderer sees them. Do not parse identity as ANSI markup.
	owner = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, owner)
	_, err = tea.NewProgram(settings{owner: owner, width: width, height: height},
		tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Settings:", err)
		os.Exit(1)
	}
}
