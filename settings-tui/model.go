package main

// The management model: one row per trusted box template, refreshed from the
// Kubernetes API through the small boxClient seam, with a single delete
// action behind a confirmation dialog. Quitting, crashing, or failing here
// never exposes a shell; this application is the only program in the image.

import (
	"context"
	"errors"

	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// pollInterval is the automatic refresh cadence.
	pollInterval = 3 * time.Second
	// refreshBudget bounds one whole refresh round (all gets run in
	// parallel inside it).
	refreshBudget = 2 * time.Second
)

// connState is the honesty of the displayed rows.
type connState int

const (
	connChecking connState = iota
	connLive
	connPartial
	connUnreachable
)

func (s connState) String() string {
	switch s {
	case connLive:
		return "Live"
	case connPartial:
		return "Partial"
	case connUnreachable:
		return "Unreachable"
	default:
		return "checking…"
	}
}

// rowState is what one box row shows.
type rowState int

const (
	rowChecking rowState = iota
	rowRunning
	rowPending
	rowTerminating
	rowError
	rowNotCreated
	rowUnavailable // never seen; the API could not be reached
	rowForbidden   // authorization refused; reconnect settings
)

// boxRow is one box template with its last-known pod state.
type boxRow struct {
	template     string
	name         string
	state        rowState
	stale        bool // last-known data; the latest refresh failed
	reconnect    bool // authorization refused; reconnect settings to refresh
	phase        string
	started      time.Time
	image        string
	runtimeClass string
	restarts     int32
	requests     string
	limits       string
}

// boxExists reports whether the row currently represents a live pod object.
// Rows that never saw their pod (checking, forbidden, unavailable) cannot
// vouch for existence; a forbidden row only knows authorization was refused.
func (r boxRow) boxExists() bool {
	switch r.state {
	case rowNotCreated, rowChecking, rowUnavailable, rowForbidden:
		return false
	default:
		return true
	}
}

type dialogState int

const (
	dialogNone dialogState = iota
	dialogConfirmDelete
	dialogDeleting
)

// rowResult is one template's outcome of a refresh round.
type rowResult struct {
	pod *podInfo
	err error
}

// tickMsg re-triggers the periodic refresh.
type tickMsg time.Time

// refreshDoneMsg applies one completed refresh round.
type refreshDoneMsg struct {
	results []rowResult
}

// deleteDoneMsg reports the outcome of a confirmed delete.
type deleteDoneMsg struct {
	err error
}

// settingsModel is the Bubble Tea model of the management UI. owner is the
// canonical (trimmed) verified identity: it is hashed into box names exactly
// like the config server does and is sanitized only at display time.
type settingsModel struct {
	owner      string
	maxBoxes   int // trusted per-owner cap; 0 disables the cap element
	client     boxClient
	rows       []boxRow
	selected   int
	conn       connState
	dialog     dialogState
	message    string
	width      int
	height     int
	refreshing bool
	nowFunc    func() time.Time
}

// newModel builds the initial model from trusted inputs.
func newModel(owner string, templates []string, maxBoxes int, client boxClient, width, height int) settingsModel {
	m := settingsModel{
		owner:    owner,
		maxBoxes: maxBoxes,
		client:   client,
		rows:     make([]boxRow, len(templates)),
		conn:     connChecking,
		width:    width,
		height:   height,
		nowFunc:  time.Now,
	}
	for i, template := range templates {
		m.rows[i] = boxRow{template: template, name: boxPodName(owner, template), state: rowChecking}
	}
	return m
}

func (m settingsModel) Init() tea.Cmd {
	return tea.Batch(refresh(m), tick())
}

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refresh issues one get per trusted template name, in parallel, inside one
// budget; the result is applied as a whole.
func refresh(m settingsModel) tea.Cmd {
	client, rows := m.client, m.rows
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.name
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), refreshBudget)
		defer cancel()
		results := make([]rowResult, len(names))
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				pod, err := client.GetPod(ctx, name)
				results[i] = rowResult{pod: pod, err: err}
			}(i, name)
		}
		wg.Wait()
		return refreshDoneMsg{results: results}
	}
}

// delete issues the confirmed pod delete. A 404 means the goal state.
func (m settingsModel) delete(row boxRow) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		err := client.DeletePod(ctx, row.name)
		if errors.Is(err, errNotFound) {
			err = nil
		}
		return deleteDoneMsg{err: err}
	}
}

func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tick())
		if !m.refreshing && len(m.rows) > 0 {
			m.refreshing = true
			cmds = append(cmds, refresh(m))
		}
		return m, tea.Batch(cmds...)

	case refreshDoneMsg:
		m.refreshing = false
		m.applyRefresh(msg.results)
		return m, nil

	case deleteDoneMsg:
		m.dialog = dialogNone
		if msg.err != nil {
			if errors.Is(msg.err, errForbidden) {
				m.message = "delete refused (authorization changed) — reconnect settings to refresh"
			} else {
				m.message = "delete failed: " + msg.err.Error()
			}
		} else {
			m.message = ""
		}
		// Never act on, or display, pre-delete state: refresh immediately.
		if len(m.rows) > 0 {
			m.refreshing = true
			return m, refresh(m)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// applyRefresh folds one refresh round into the rows and the connection
// status. Failures keep the last-known row (rendered dim); 404 means the box
// is not created; 403 keeps last-known plus a reconnect hint.
func (m *settingsModel) applyRefresh(results []rowResult) {
	if len(results) != len(m.rows) {
		return
	}
	ok, failed := 0, 0
	for i, result := range results {
		row := &m.rows[i]
		switch {
		case result.err == nil:
			ok++
			row.apply(result.pod)
		case errors.Is(result.err, errNotFound):
			ok++
			row.state = rowNotCreated
			row.stale, row.reconnect = false, false
			row.phase, row.image, row.runtimeClass, row.requests, row.limits = "", "", "", "", ""
			row.started, row.restarts = time.Time{}, 0
		case errors.Is(result.err, errForbidden):
			failed++
			// Authorization changed mid-session: keep the last-known data dim,
			// but the reconnect hint is the actionable information.
			row.reconnect = true
			row.stale = row.phase != ""
			if row.state == rowChecking {
				row.state = rowForbidden
			}
		default:
			failed++
			row.reconnect = false
			row.stale = row.phase != ""
			if row.state == rowChecking {
				row.state = rowUnavailable
			}
		}
	}
	switch {
	case failed == 0:
		m.conn = connLive
	case ok == 0:
		m.conn = connUnreachable
	default:
		m.conn = connPartial
	}
	if m.conn == connLive {
		m.message = ""
	}
	m.selected = clamp(m.selected, len(m.rows))
}

func (r *boxRow) apply(pod *podInfo) {
	r.stale, r.reconnect = false, false
	r.phase = pod.Status.Phase
	switch {
	case pod.Metadata.DeletionTimestamp != nil:
		r.state = rowTerminating
	case pod.Status.Phase == "Running":
		r.state = rowRunning
	case pod.Status.Phase == "" || pod.Status.Phase == "Pending":
		r.state = rowPending
	default:
		r.state = rowError
	}
	if pod.Status.StartTime != nil {
		r.started = *pod.Status.StartTime
	} else {
		r.started = time.Time{}
	}
	if len(pod.Spec.Containers) > 0 {
		container := pod.Spec.Containers[0]
		r.image = container.Image
		r.requests = formatMemCPU(container.Resources.Requests)
		r.limits = formatMemCPU(container.Resources.Limits)
	} else {
		r.image, r.requests, r.limits = "", "—", "—"
	}
	r.runtimeClass = "default"
	if pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName != "" {
		r.runtimeClass = *pod.Spec.RuntimeClassName
	}
	var restarts int32
	for _, status := range pod.Status.ContainerStatuses {
		restarts += status.RestartCount
	}
	r.restarts = restarts
}

func (m settingsModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.dialog {
	case dialogConfirmDelete, dialogDeleting:
		return m.dialogKey(msg.String())
	default:
		return m.listKey(msg.String())
	}
}

func (m settingsModel) listKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.rows)-1 {
			m.selected++
		}
	case "r":
		if !m.refreshing && len(m.rows) > 0 {
			m.refreshing = true
			return m, refresh(m)
		}
	case "d":
		if len(m.rows) == 0 {
			m.message = "no box templates exist — nothing to delete"
			return m, nil
		}
		if m.conn != connLive {
			m.message = "delete is unavailable while the connection is " + m.conn.String() + " (stale state)"
			return m, nil
		}
		row := m.rows[m.selected]
		if !row.boxExists() {
			m.message = row.template + " is not created; log in as " + row.template + "@<host> to create it"
			return m, nil
		}
		m.message = ""
		m.dialog = dialogConfirmDelete
	}
	return m, nil
}

func (m settingsModel) dialogKey(key string) (tea.Model, tea.Cmd) {
	if m.dialog == dialogDeleting {
		return m, nil // a delete is in flight; ignore input until it settles
	}
	switch key {
	case "y":
		if m.conn != connLive {
			// Never act on stale information, even if it went stale mid-dialog.
			m.dialog = dialogNone
			m.message = "delete cancelled: the connection is " + m.conn.String()
			return m, nil
		}
		row := m.rows[m.selected]
		m.dialog = dialogDeleting
		return m, m.delete(row)
	case "n", "esc", "q", "ctrl+c":
		m.dialog = dialogNone
		return m, nil
	}
	return m, nil
}

func clamp(index, length int) int {
	if length == 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

// createdCount is how many of the owner's listed boxes currently exist as
// pods (any phase, including Terminating). It is advisory display data for
// the cap line: the config server's cap check lists all live owner pods and
// stays authoritative, so boxes from templates no longer published to this
// owner can make the true count higher than what the UI can see.
func (m settingsModel) createdCount() int {
	n := 0
	for _, row := range m.rows {
		if row.boxExists() {
			n++
		}
	}
	return n
}
