package main

// Rendering: plain text plus optional SGR colors, never any other escape
// sequence. All dynamic text (owner, images, runtime classes, API messages)
// is sanitized to printable runes before it reaches a terminal, and every
// line is width-truncated.
//
// The normal layout is master–detail: a master
// pane listing each trusted template with only its state, footed by the
// per-owner box cap, and a detail pane for the selected box with a status
// strip (status, uptime, restarts) over a ledger (image, runtime, requests,
// limits). Both panes share the taller pane's height. The delete confirmation
// is a centered popup over a blank body.
// Terminals without room for the panes collapse to the older single-column
// rows, then compact headers, and at one/two rows the footer keeps the quit
// hint; the footer always carries the owner and connection when it fits.

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const sshHostPlaceholder = "<host>"

// Layout metrics of the master–detail layout.
const (
	// stateWidth is the widest state word ("Terminating", "not created",
	// "unreachable"); master rows right-align the state in this column.
	stateWidth = 11
	// stripWidth is the status strip (STATUS 12, UPTIME 11, RESTARTS).
	stripWidth = 31
	// ledgerLabelWidth is the detail ledger label column ("REQUESTS" + gap).
	ledgerLabelWidth = 10
	// barCells is the width of the cap bar in block cells.
	barCells = 10
)

// Honesty lines of the detail pane; the only lines dropped when the pane
// does not fit (the dim master row and the connection status still carry it).
const (
	honestyLastKnown = "last known state — refresh failed"
	honestyReconnect = "authorization changed — reconnect settings to refresh"
)

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

// renderLines builds the visible lines for the current size. The footer is
// always the last line; the message line, when set, is always second to last.
func (m settingsModel) renderLines() []string {
	style := styler{on: colorsEnabled()}
	switch {
	case m.height <= 1:
		return []string{m.footer(style, false)}
	case m.height == 2:
		if m.dialog != dialogNone {
			return []string{m.dialogCompact(style), m.footer(style, false)}
		}
		return []string{m.selectedLine(style), m.footer(style, false)}
	}

	// Body budget: everything above the footer (and the message line).
	budget := m.height - 1
	if m.message != "" {
		budget--
	}
	if budget < 1 {
		budget = 1
	}

	// The master–detail layout needs something to list and room for the
	// master pane; everything smaller falls through to the older layouts.
	if len(m.rows) > 0 && budget >= 6 {
		if lines, ok := m.masterDetailLines(style, budget); ok {
			if m.message != "" {
				lines = append(lines, style.red(sanitize(m.message)))
			}
			return append(lines, m.footer(style, true))
		}
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
	bodyBudget := m.height - len(header) - 1
	if m.message != "" {
		bodyBudget--
	}
	if bodyBudget < 1 {
		bodyBudget = 1
	}
	var body []string
	if m.dialog != dialogNone {
		body = m.dialogLines(style, bodyBudget)
	} else {
		body = m.bodyLines(style, bodyBudget)
	}
	lines := append(header, body...)
	if m.message != "" {
		lines = append(lines, style.red(sanitize(m.message)))
	}
	return append(lines, m.footer(style, false))
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

// stateWord is the plain state word shown in the master list and the detail
// strip; the older single-column rows add honesty suffixes via statusText.
func (r boxRow) stateWord() string {
	switch r.state {
	case rowRunning:
		return "Running"
	case rowPending:
		return "Pending"
	case rowTerminating:
		return "Terminating"
	case rowError:
		return "Error"
	case rowNotCreated:
		return "not created"
	case rowForbidden:
		return "reconnect"
	case rowUnavailable:
		return "unreachable"
	default:
		return "…"
	}
}

// statusText is the plain status column of the older single-column rows. The
// reconnect hint wins over the generic last-known suffix: it is the
// actionable information.
func (r boxRow) statusText() string {
	status := r.stateWord()
	switch {
	case r.reconnect && status != "reconnect":
		status += " (reconnect)"
	case r.stale:
		status += " (last known)"
	}
	return status
}

// scrollRows keeps the selected row visible when the rows do not fit.
func scrollRows(rows []string, visible, selected int) []string {
	if len(rows) <= visible {
		return rows
	}
	top := selected - visible + 1
	if top < 0 {
		top = 0
	}
	if top+visible > len(rows) {
		top = len(rows) - visible
	}
	return rows[top : top+visible]
}

// masterDetailLines renders the master–detail body (or its centered delete
// popup) and reports whether the layout fits at all. Both panes fill the body
// budget: blank rows grow above each pane's footed block, so the boxes close
// on the same line and the footer sits at the terminal bottom.
func (m settingsModel) masterDetailLines(style styler, budget int) ([]string, bool) {
	if m.dialog != dialogNone {
		if budget >= 8 {
			return m.deletePopup(style, budget)
		}
		return nil, false
	}
	masterInner, detailInner := m.paneWidths(budget)
	if masterInner == 0 {
		return nil, false
	}
	master := m.masterPane(style, masterInner, budget)
	master = stretchPane(master, budget-len(master), masterInner)
	if detailInner > 0 {
		detail := m.detailPane(style, detailInner, budget)
		detail = stretchPane(detail, budget-len(detail), detailInner)
		return zipPanes(master, detail), true
	}
	return master, true
}

// paneWidths returns the master and detail inner widths (detail 0 when the
// detail pane does not fit; both 0 when even the master pane cannot render).
// The detail width covers the widest row so it does not jump between
// selections.
func (m settingsModel) paneWidths(budget int) (int, int) {
	templateWidth := 6
	for _, row := range m.rows {
		if n := len(row.template); n > templateWidth {
			templateWidth = n
		}
	}
	masterInner := 2 + templateWidth + 2 + stateWidth
	if budget < 6 || m.width < masterInner+4 {
		return 0, 0
	}
	detail := stripWidth
	for _, row := range m.rows {
		title := sanitize(row.template) + " · " + row.name
		detail = max(detail, ansi.StringWidth(title))
		for _, value := range []string{row.image, row.runtimeClass, row.requests, row.limits} {
			detail = max(detail, ledgerLabelWidth+ansi.StringWidth(sanitize(value)))
		}
		detail = max(detail,
			ansi.StringWidth("ssh "+sanitize(row.template)+"@"+sshHostPlaceholder+" creates this box"),
			ansi.StringWidth(honestyReconnect))
	}
	// One gutter column and the two detail borders beside the master pane.
	avail := m.width - (masterInner + 4) - 1 - 4
	if avail < stripWidth || budget < 9 {
		return masterInner, 0
	}
	return masterInner, min(detail, avail)
}

// masterPane renders the boxes pane: one row per trusted template — name
// left, state right — footed by the per-owner cap when one is set.
func (m settingsModel) masterPane(style styler, inner, budget int) []string {
	chrome := 2 // top and bottom border
	if m.maxBoxes > 0 {
		chrome += 2 // separator and cap line
		if m.createdCount() >= m.maxBoxes {
			chrome++ // the at-cap hint
		}
	}
	rows := scrollRows(m.masterRowLines(style), max(1, budget-chrome), m.selected)
	lines := make([]string, 0, budget)
	lines = append(lines, paneTop("boxes", inner))
	for _, row := range rows {
		lines = append(lines, paneLine(row, inner))
	}
	if m.maxBoxes > 0 {
		lines = append(lines, paneSep(inner), paneLine(m.capLine(style), inner))
		if m.createdCount() >= m.maxBoxes {
			lines = append(lines, paneLine(style.dim("cap reached — delete one"), inner))
		}
	}
	return append(lines, paneBottom(inner))
}

// masterRowLines renders one master row per template: the name left, the
// state right, and nothing else. Selection is the marker plus a bold name;
// stale or reconnect rows render dim.
func (m settingsModel) masterRowLines(style styler) []string {
	templateWidth := 6
	for _, row := range m.rows {
		if n := len(row.template); n > templateWidth {
			templateWidth = n
		}
	}
	lines := make([]string, len(m.rows))
	for i, row := range m.rows {
		marker, name := "  ", sanitize(row.template)
		if i == m.selected {
			marker = "▸ "
			name = style.bold(name)
		}
		state := row.stateWord()
		switch row.state {
		case rowRunning:
			state = style.green(state)
		case rowPending, rowTerminating:
			state = style.yellow(state)
		case rowError:
			state = style.red(state)
		default:
			state = style.dim(state)
		}
		line := padTo(marker+name, 2+templateWidth) + "  " + rightIn(state, stateWidth)
		if row.stale || row.reconnect {
			line = style.dim(line)
		}
		lines[i] = line
	}
	return lines
}

// capLine renders the per-owner box cap: "created N/max" with a block bar of
// the used fraction, yellow at the cap. The count is advisory display data;
// the config server's cap check stays authoritative.
func (m settingsModel) capLine(style styler) string {
	created := m.createdCount()
	filled := min(created*barCells/m.maxBoxes, barCells)
	tint := style.green
	if created >= m.maxBoxes {
		tint = style.yellow
	}
	bar := tint(strings.Repeat("█", filled))
	if rest := barCells - filled; rest > 0 {
		bar += style.dim(strings.Repeat("░", rest))
	}
	return "created " + style.bold(fmt.Sprintf("%d/%d", created, m.maxBoxes)) + " " + bar
}

// detailPane renders the selected box's detail pane: rows without data
// explain themselves, data rows get a status strip (status, uptime, restarts)
// over a ledger (image, runtime, requests, limits).
func (m settingsModel) detailPane(style styler, inner, budget int) []string {
	row := m.rows[clamp(m.selected, len(m.rows))]
	title := sanitize(row.template) + " · " + row.name
	lines := []string{paneTop(title, inner)}
	switch row.state {
	case rowNotCreated:
		lines = append(lines,
			paneLine(style.dim("not created"), inner),
			paneLine(style.dim("ssh "+sanitize(row.template)+"@"+sshHostPlaceholder+" creates this box"), inner))
	case rowChecking:
		lines = append(lines, paneLine(style.dim("checking…"), inner))
	case rowForbidden:
		lines = append(lines,
			paneLine(style.dim("authorization changed"), inner),
			paneLine(style.dim("reconnect settings to refresh"), inner))
	case rowUnavailable:
		lines = append(lines,
			paneLine(style.dim("unreachable"), inner),
			paneLine(style.dim("retrying — last known state when it returns"), inner))
	default:
		status := row.stateWord()
		switch row.state {
		case rowRunning:
			status = style.green(status)
		case rowPending, rowTerminating:
			status = style.yellow(status)
		case rowError:
			status = style.red(status)
		}
		strip := []string{
			style.dim(padTo("STATUS", 12) + padTo("UPTIME", 11) + "RESTARTS"),
			padTo(status, 12) + padTo(formatUptime(row.started, m.nowFunc()), 11) + fmt.Sprint(row.restarts),
		}
		honesty := ""
		switch {
		case row.reconnect:
			honesty = honestyReconnect
		case row.stale:
			honesty = honestyLastKnown
		}
		ledger := make([]string, 0, 4)
		for _, entry := range []struct{ label, value string }{
			{"IMAGE", sanitize(row.image)},
			{"RUNTIME", sanitize(row.runtimeClass)},
			{"REQUESTS", sanitize(row.requests)},
			{"LIMITS", sanitize(row.limits)},
		} {
			ledger = append(ledger, style.dim(padTo(entry.label, ledgerLabelWidth))+entry.value)
		}
		// Borders + separator + strip + ledger is the full pane; only the
		// honesty line may be dropped when the pane does not fit (the check
		// counts the honesty line itself).
		if honesty != "" && len(strip)+len(ledger)+4 > budget {
			honesty = ""
		}
		for _, line := range strip {
			lines = append(lines, paneLine(line, inner))
		}
		if honesty != "" {
			lines = append(lines, paneLine(style.dim(honesty), inner))
		}
		lines = append(lines, paneSep(inner))
		for _, line := range ledger {
			lines = append(lines, paneLine(line, inner))
		}
	}
	return append(lines, paneBottom(inner))
}

// deletePopup renders the delete confirmation as a popup centered over a blank
// body. All three consequences are always stated.
func (m settingsModel) deletePopup(style styler, budget int) ([]string, bool) {
	row := m.rows[clamp(m.selected, len(m.rows))]
	content := []string{
		style.bold(`Delete box "` + sanitize(row.template) + `" (` + row.name + `)?`),
		style.dim("· all data on the box's local disk is destroyed"),
		style.dim("· active sessions in the box are killed"),
		style.dim("· a fresh box is created on your next login:"),
		style.dim("  ssh " + sanitize(row.template) + "@" + sshHostPlaceholder),
	}
	if budget >= 9 {
		content = append(content, "")
	}
	content = append(content, style.red("y delete")+" · "+style.dim("n/Esc cancel"))
	inner := 0
	for _, line := range content {
		inner = max(inner, ansi.StringWidth(line))
	}
	if inner+4 > m.width {
		return nil, false // fall back to the in-body dialog
	}
	popup := []string{paneTop("delete box", inner)}
	for _, line := range content {
		popup = append(popup, paneLine(line, inner))
	}
	popup = append(popup, paneBottom(inner))
	indent := strings.Repeat(" ", max(0, (m.width-(inner+4))/2))
	lines := make([]string, 0, budget)
	for i := 0; i < (budget-len(popup))/2; i++ {
		lines = append(lines, "")
	}
	for _, line := range popup {
		lines = append(lines, indent+line)
	}
	return lines, true
}

// stretchPane grows a pane by blank inner lines above its footed block —
// everything from the pane's separator (the master cap separator, the detail
// ledger separator) down to its bottom border — so paired panes can close on
// the same line and fill the body budget. A pane without a separator pads
// above its bottom border. Content lines always start with the pane's side
// border, so the separator is unambiguous.
func stretchPane(lines []string, grow, inner int) []string {
	if grow <= 0 {
		return lines
	}
	at := len(lines) - 1
	for i, line := range lines {
		if strings.HasPrefix(line, "├") {
			at = i
			break
		}
	}
	stretched := make([]string, 0, len(lines)+grow)
	stretched = append(stretched, lines[:at]...)
	blank := paneLine("", inner)
	for i := 0; i < grow; i++ {
		stretched = append(stretched, blank)
	}
	return append(stretched, lines[at:]...)
}

// zipPanes joins two panes side by side, one gutter column apart, padding
// the shorter pane's rows with blanks.
func zipPanes(left, right []string) []string {
	leftWidth := 0
	for _, line := range left {
		leftWidth = max(leftWidth, ansi.StringWidth(line))
	}
	blank := strings.Repeat(" ", leftWidth)
	lines := make([]string, max(len(left), len(right)))
	for i := range lines {
		l, r := blank, ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines[i] = l + " " + r
	}
	return lines
}

// paneLine frames one content line inside a pane: border, inner padding, and
// width-exact truncation, so every pane keeps a closed right edge.
func paneLine(content string, inner int) string {
	if width := ansi.StringWidth(content); width > inner {
		content = ansi.Truncate(content, inner, "…")
	} else {
		content = padTo(content, inner)
	}
	return "│ " + content + " │"
}

// paneTop is a pane's top border with an embedded title.
func paneTop(title string, inner int) string {
	title = sanitize(title)
	if max := inner - 2; max < 1 {
		return "┌" + strings.Repeat("─", inner+2) + "┐"
	} else if t := ansi.StringWidth(title); t > max {
		title = ansi.Truncate(title, max, "…")
	}
	return "┌─ " + title + " " + strings.Repeat("─", inner-1-ansi.StringWidth(title)) + "┐"
}

func paneBottom(inner int) string { return "└" + strings.Repeat("─", inner+2) + "┘" }

func paneSep(inner int) string { return "├" + strings.Repeat("─", inner+2) + "┤" }

// padTo pads text with spaces to a display width (SGR-aware); longer text is
// returned unchanged (callers truncate where a hard cap is needed).
func padTo(text string, width int) string {
	if gap := width - ansi.StringWidth(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// rightIn right-aligns text inside a display width.
func rightIn(text string, width int) string {
	if gap := width - ansi.StringWidth(text); gap > 0 {
		return strings.Repeat(" ", gap) + text
	}
	return text
}

// bodyLines renders the box rows (or the empty state) of the older
// single-column layout inside the budget, keeping the selected row visible
// when the list does not fit.
func (m settingsModel) bodyLines(style styler, budget int) []string {
	if len(m.rows) == 0 {
		empty := []string{"No box templates are available."}
		if budget > 1 {
			empty = append(empty, style.dim("Each published template becomes a box: ssh <template>@"+sshHostPlaceholder))
		}
		return empty
	}
	return scrollRows(m.rowLines(style), budget, m.selected)
}

// rowLines renders every box row of the older single-column layout. The
// selection marker and template name are contiguous so selection stays
// visible in plain text.
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

// dialogLines renders the delete confirmation inside the body budget of the
// older layouts (the master–detail layout centers a popup instead). The
// three consequences are always stated; cramped terminals collapse them onto
// one line, and the smallest budgets keep prompt plus keys.
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

// footer renders the key hints; identity=true (master–detail layout) also
// right-aligns the verified owner and the connection state, truncating the
// owner rather than dropping it.
func (m settingsModel) footer(style styler, identity bool) string {
	var keys string
	switch {
	case m.width >= 44:
		keys = "↑/↓ select · r refresh · d delete · q: quit"
	case m.width >= 22:
		keys = "j/k · r · d · q: quit"
	default:
		return "q: quit"
	}
	if !identity {
		return keys
	}
	suffix := " · " + m.connStyled(style)
	avail := m.width - ansi.StringWidth(keys) - 2 - len("Owner: ") - ansi.StringWidth(suffix)
	if avail < 1 {
		return keys
	}
	return keys + "  " + "Owner: " + ansi.Truncate(sanitize(m.owner), avail, "…") + suffix
}
