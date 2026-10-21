package main

// Rendering: plain text plus optional SGR colors, never any other escape
// sequence. All dynamic text (owner, images, runtime classes, API messages)
// is sanitized to printable runes before it reaches a terminal, and every
// line is width-truncated; tiny terminals collapse along the same pattern as
// the original POC screen.

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const sshHostPlaceholder = "<host>"

// ANSI SGR fragments. Only these styling escapes are ever emitted, and only
// when colors are enabled.
const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrRed    = "\x1b[31m"
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
)

// sanitize maps every control, format (bidi), or non-printing rune to '?'
// before it can reach a terminal. Identity and API strings are data, never
// markup.
func sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, text)
}

// colorsEnabled honors NO_COLOR and dumb/unset terminals.
func colorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	term := os.Getenv("TERM")
	return term != "" && term != "dumb"
}

type styler struct{ on bool }

func (s styler) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return code + text + sgrReset
}

func (s styler) dim(text string) string    { return s.wrap(sgrDim, text) }
func (s styler) bold(text string) string   { return s.wrap(sgrBold, text) }
func (s styler) red(text string) string    { return s.wrap(sgrRed, text) }
func (s styler) green(text string) string  { return s.wrap(sgrGreen, text) }
func (s styler) yellow(text string) string { return s.wrap(sgrYellow, text) }

func (m settingsModel) View() tea.View {
	lines := m.renderLines()
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(1, m.width), "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// renderLines builds the visible lines for the current size, from the full
// layout down to a one-line footer. The footer is always the last line.
func (m settingsModel) renderLines() []string {
	style := styler{on: colorsEnabled()}
	switch {
	case m.height <= 1:
		return []string{m.footer()}
	case m.height == 2:
		if m.dialog != dialogNone {
			return []string{m.dialogCompact(style), m.footer()}
		}
		return []string{m.selectedLine(style), m.footer()}
	}

	compact := m.height < 7
	var header []string
	if compact {
		header = []string{m.headerLine(style)}
	} else {
		header = []string{
			style.bold("dev.box settings"),
			"Owner: " + style.bold(sanitize(m.owner)),
			"Connection: " + m.connStyled(style),
		}
	}
	// Body budget: everything the chrome needs, minus the body itself.
	budget := m.height - len(header) - 1
	if m.message != "" {
		budget--
	}
	if budget < 1 {
		budget = 1
	}
	var body []string
	if m.dialog != dialogNone {
		body = m.dialogLines(style, budget)
	} else {
		body = m.bodyLines(style, budget)
	}
	lines := append(header, body...)
	if m.message != "" {
		lines = append(lines, style.red(sanitize(m.message)))
	}
	return append(lines, m.footer())
}

func (m settingsModel) headerLine(style styler) string {
	return strings.Join([]string{
		"settings",
		"Owner: " + sanitize(m.owner),
		m.connStyled(style),
	}, " · ")
}

func (m settingsModel) connStyled(style styler) string {
	switch m.conn {
	case connLive:
		return style.green(m.conn.String())
	case connPartial:
		return style.yellow(m.conn.String())
	case connUnreachable:
		return style.red(m.conn.String())
	default:
		return style.dim(m.conn.String())
	}
}

// bodyLines renders the box rows (or the empty state) inside the budget,
// keeping the selected row visible when the list does not fit.
func (m settingsModel) bodyLines(style styler, budget int) []string {
	if len(m.rows) == 0 {
		empty := []string{"No box templates are available."}
		if budget > 1 {
			empty = append(empty, style.dim("Each published template becomes a box: ssh <template>@"+sshHostPlaceholder))
		}
		return empty
	}
	rows := m.rowLines(style)
	if len(rows) <= budget {
		return rows
	}
	top := m.selected - budget + 1
	if top < 0 {
		top = 0
	}
	return rows[top : top+budget]
}

// statusText is the plain status column for a row. The reconnect hint wins
// over the generic last-known suffix: it is the actionable information.
func (r boxRow) statusText() string {
	var status string
	switch r.state {
	case rowRunning:
		status = "Running"
	case rowPending:
		status = "Pending"
	case rowTerminating:
		status = "Terminating"
	case rowError:
		status = "Error"
	case rowNotCreated:
		status = "not created"
	case rowForbidden:
		status = "reconnect"
	case rowUnavailable:
		status = "unreachable"
	default:
		status = "…"
	}
	switch {
	case r.reconnect && status != "reconnect":
		status += " (reconnect)"
	case r.stale:
		status += " (last known)"
	}
	return status
}

// rowLines renders every box row. The selection marker and template name are
// contiguous so selection stays visible in plain text.
func (m settingsModel) rowLines(style styler) []string {
	templateWidth := 8
	for _, row := range m.rows {
		if n := len(row.template); n > templateWidth {
			templateWidth = n
		}
	}
	lines := make([]string, len(m.rows))
	for i, row := range m.rows {
		marker := "  "
		if i == m.selected {
			marker = "▸ "
		}
		prefix := fmt.Sprintf("%s%-*s", marker, templateWidth+2, row.template)
		var line string
		switch {
		case row.state == rowNotCreated:
			line = prefix + style.dim("not created · ssh "+row.template+"@"+sshHostPlaceholder)
		case row.state == rowChecking:
			line = prefix + style.dim("…")
		case row.state == rowUnavailable:
			line = style.dim(prefix + "unreachable")
		case row.state == rowForbidden && row.phase == "":
			line = style.dim(prefix + "reconnect · authorization changed")
		default:
			status := row.statusText()
			switch row.state {
			case rowRunning:
				status = style.green(status)
			case rowPending, rowTerminating:
				status = style.yellow(status)
			case rowError:
				status = style.red(status)
			}
			details := fmt.Sprintf("%-*s %s  %s  %s  restarts %d  %s", 20, status,
				formatUptime(row.started, m.nowFunc()),
				sanitize(row.image), sanitize(row.runtimeClass), row.restarts, sanitize(row.limits))
			if row.stale || row.reconnect {
				line = style.dim(prefix + details)
			} else {
				line = prefix + details
			}
		}
		lines[i] = line
	}
	return lines
}

// selectedLine is the one-row view for two-line terminals.
func (m settingsModel) selectedLine(style styler) string {
	if len(m.rows) == 0 {
		return style.dim("no box templates")
	}
	return m.rowLines(style)[m.selected]
}

// dialogLines renders the delete confirmation inside the budget. The three
// consequences are always stated; cramped terminals collapse them onto one
// line, and the smallest budgets keep prompt plus keys.
func (m settingsModel) dialogLines(style styler, budget int) []string {
	row := m.rows[clamp(m.selected, len(m.rows))]
	yes := style.red("y delete")
	cancel := style.dim("n/Esc cancel")
	if budget >= 5 {
		return []string{
			style.bold("Delete box \"" + row.template + "\" (" + row.name + ")?"),
			style.dim("· all data on the box's local disk is destroyed"),
			style.dim("· active sessions in the box are killed"),
			style.dim("· a fresh box is created on your next login: ssh " + row.template + "@" + sshHostPlaceholder),
			yes + " · " + cancel,
		}
	}
	prompt := style.bold("Delete box \"" + row.template + "\" (" + row.name + ")?")
	if budget >= 3 {
		return []string{
			prompt,
			style.dim("· destroys its local disk · kills its sessions · recreated on next login"),
			yes + " · " + cancel,
		}
	}
	if budget == 2 {
		return []string{prompt, yes + " · " + cancel}
	}
	return []string{prompt}
}

// dialogCompact is the two-line-terminal dialog.
func (m settingsModel) dialogCompact(style styler) string {
	row := m.rows[clamp(m.selected, len(m.rows))]
	return style.bold("Delete "+row.template+"?") + " " + style.red("y delete") + " · " + style.dim("n/Esc cancel")
}

func (m settingsModel) footer() string {
	switch {
	case m.width >= 44:
		return "↑/↓ select · r refresh · d delete · q: quit"
	case m.width >= 22:
		return "j/k · r · d · q: quit"
	default:
		return "q: quit"
	}
}
