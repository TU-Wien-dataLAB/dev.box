//go:build unix

package main

// Offline management-logic tests against a fake Kubernetes client: row
// assembly from gets, 404/403/error handling, delete confirmation flow,
// Terminating transition, polling, and connection-status honesty. These drive
// the real model + renderer, not internal helpers.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeClient struct {
	pods     map[string]*podInfo
	failures map[string]error
	deleted  []string
	delErr   error
}

func (f *fakeClient) GetPod(_ context.Context, name string) (*podInfo, error) {
	if err, ok := f.failures[name]; ok {
		return nil, err
	}
	pod, ok := f.pods[name]
	if !ok {
		return nil, errNotFound
	}
	return pod, nil
}

func (f *fakeClient) DeletePod(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.pods, name)
	return nil
}

func runningPod(name string) *podInfo {
	return podFromJSON(`{
		"metadata": {"name": "` + name + `"},
		"spec": {
			"runtimeClassName": "kata-qemu-runtime-rs",
			"containers": [{"name": "shell", "image": "containerssh/containerssh-guest-image",
				"resources": {"requests": {"cpu": "250m", "memory": "512Mi"},
					"limits": {"cpu": "500m", "memory": "1Gi"}}}]
		},
		"status": {
			"phase": "Running",
			"startTime": "` + time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339) + `",
			"containerStatuses": [{"restartCount": 1}]
		}
	}`)
}

func terminatingPod(name string) *podInfo {
	pod := runningPod(name)
	pod.Metadata.DeletionTimestamp = ptr(time.Now())
	return pod
}

func podFromJSON(data string) *podInfo {
	var pod podInfo
	if err := json.Unmarshal([]byte(data), &pod); err != nil {
		panic(err)
	}
	return &pod
}

func ptr[T any](v T) *T { return &v }

// driveRefresh runs one refresh round synchronously, like the program would.
func driveRefresh(t *testing.T, m settingsModel) settingsModel {
	t.Helper()
	cmd := refresh(m)
	msg, ok := cmd().(refreshDoneMsg)
	if !ok {
		t.Fatalf("refresh command produced %T", cmd())
	}
	next, _ := m.Update(msg)
	return next.(settingsModel)
}

func press(t *testing.T, m settingsModel, key tea.KeyPressMsg) settingsModel {
	t.Helper()
	next, _ := m.Update(key)
	return next.(settingsModel)
}

// confirmPress presses y and runs the delete command it returns.
func confirmPress(t *testing.T, m settingsModel) settingsModel {
	t.Helper()
	next, cmd := m.Update(key('y'))
	m = next.(settingsModel)
	if cmd == nil {
		t.Fatal("confirm did not issue a delete")
	}
	msg, ok := cmd().(deleteDoneMsg)
	if !ok {
		t.Fatalf("delete command produced %T", cmd())
	}
	next, _ = m.Update(msg)
	return next.(settingsModel)
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// view returns the rendered view with styling stripped so assertions are
// color-insensitive; colors are covered separately.
func view(m settingsModel) string {
	return ansi.Strip(m.View().Content)
}

func TestBoxNamesMatchTheConfigServerContract(t *testing.T) {
	// Golden vectors: the first is the live cluster's real ubuntu box for the
	// real owner, proving the TUI recomputes the exact persistent names.
	if got := boxPodName("matthias.matt", "ubuntu"); got != "box-8bbfc143fa" {
		t.Fatalf("box name = %q, want box-8bbfc143fa", got)
	}
	if got := boxPodName("alice", "ubuntu"); got != "box-ec061c6832" {
		t.Fatalf("box name = %q, want box-ec061c6832", got)
	}
}

func TestParseTemplates(t *testing.T) {
	if got, err := parseTemplates(""); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	got, err := parseTemplates(" zeta , ubuntu,,zeta,alpha ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "alpha,ubuntu,zeta" {
		t.Fatalf("templates = %v, want sorted deduped list", got)
	}
	for _, bad := range []string{"bad name", "a/b", "\x1b[31m"} {
		if _, err := parseTemplates(bad); err == nil {
			t.Fatalf("invalid template list %q accepted", bad)
		}
	}
}

func TestRefreshAssemblesRowsFromGets(t *testing.T) {
	client := &fakeClient{pods: map[string]*podInfo{
		boxPodName("alice", "ubuntu"): runningPod(boxPodName("alice", "ubuntu")),
	}}
	m := newModel("alice", []string{"ubuntu", "data-engine"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	if m.conn != connLive {
		t.Fatalf("connection = %v, want Live", m.conn)
	}
	text := view(m)
	for _, fragment := range []string{
		"Owner: alice · Live",
		"ubuntu",
		"Running",
		"up 2h",
		"STATUS",
		"RESTARTS",
		"containerssh/containerssh-guest-image",
		"kata-qemu-runtime-rs",
		"REQUESTS",
		"512Mi/250m",
		"LIMITS",
		"1Gi/500m",
		"data-engine",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("row render missing %q in:\n%s", fragment, text)
		}
	}
	// The detail pane follows the selection: data-engine (not created) names
	// the create command.
	m = press(t, m, key('j'))
	if !strings.Contains(view(m), "ssh data-engine@"+sshHostPlaceholder+" creates this box") {
		t.Fatalf("not-created detail lost the create hint:\n%s", view(m))
	}
	if strings.Count(text, "▸") != 1 {
		t.Fatalf("selection marker count = %d", strings.Count(text, "▸"))
	}
}

func TestRefreshKeepsLastKnownRowsAndReportsHonestStatus(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu", "data-engine"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	if m.conn != connLive {
		t.Fatalf("connection = %v, want Live", m.conn)
	}
	// Transport failure on every row: keep the last-known rows dimmed, mark
	// Unreachable (404 counts as resolved, so both names must fail).
	client.failures = map[string]error{
		name:                               errors.New("connection refused"),
		boxPodName("alice", "data-engine"): errors.New("connection refused"),
	}
	m = driveRefresh(t, m)
	if m.conn != connUnreachable {
		t.Fatalf("connection = %v, want Unreachable", m.conn)
	}
	if !strings.Contains(view(m), "Unreachable") {
		t.Fatalf("unreachable status not shown:\n%s", view(m))
	}
	// The selected row's detail pane explains its last-known data (the
	// sorted list starts at ubuntu, which carries the stale data).
	if !strings.Contains(view(m), honestyLastKnown) {
		t.Fatalf("last-known honesty line lost:\n%s", view(m))
	}
	// Partial: one transport failure, one resolved not-created row.
	delete(client.failures, boxPodName("alice", "data-engine"))
	m = driveRefresh(t, m)
	if m.conn != connPartial {
		t.Fatalf("connection = %v, want Partial", m.conn)
	}
	if !strings.Contains(view(m), "Partial") {
		t.Fatalf("partial status not shown:\n%s", view(m))
	}
}

func TestForbiddenRowShowsReconnectHint(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{}, failures: map[string]error{name: errForbidden}}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	if m.conn != connUnreachable {
		t.Fatalf("connection = %v", m.conn)
	}
	if !strings.Contains(view(m), "reconnect") || !strings.Contains(view(m), "authorization changed") {
		t.Fatalf("reconnect hint missing:\n%s", view(m))
	}
	// Mid-session authorization change on a row with last-known data: the
	// reconnect hint must win over the generic last-known line.
	delete(client.failures, name)
	client.pods[name] = runningPod(name)
	m = driveRefresh(t, m)
	client.failures = map[string]error{name: errForbidden}
	m = driveRefresh(t, m)
	text := view(m)
	if !strings.Contains(text, honestyReconnect) {
		t.Fatalf("mid-session forbidden row lost the reconnect hint:\n%s", text)
	}
	if !strings.Contains(text, "containerssh/containerssh-guest-image") {
		t.Fatalf("last-known row data lost:\n%s", text)
	}
}

func TestTerminatingTransitionAfterDelete(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)

	// d opens the centered confirmation popup; y confirms; the delete
	// completes and an immediate refresh shows the deleting pod as Terminating.
	m = press(t, m, key('d'))
	if !strings.Contains(view(m), "Delete box \"ubuntu\" ("+name+")?") {
		t.Fatalf("confirmation missing:\n%s", view(m))
	}
	for _, consequence := range []string{
		"Local disk data is destroyed; data on retained PVCs is kept.",
		"active sessions in the box are killed",
		"a fresh box is created on your next login:",
		"ssh ubuntu@" + sshHostPlaceholder,
	} {
		if !strings.Contains(view(m), consequence) {
			t.Fatalf("confirmation misses consequence %q:\n%s", consequence, view(m))
		}
	}
	m = confirmPress(t, m)
	if len(client.deleted) != 1 || client.deleted[0] != name {
		t.Fatalf("deletes = %v", client.deleted)
	}
	client.pods[name] = terminatingPod(name)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "Terminating") {
		t.Fatalf("terminating state not shown:\n%s", view(m))
	}
	// Once the pod is gone, the row settles into the dimmed not-created form
	// and the detail pane keeps naming the create command.
	delete(client.pods, name)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "ssh ubuntu@"+sshHostPlaceholder+" creates this box") {
		t.Fatalf("post-delete detail lost the recreate hint:\n%s", view(m))
	}
}

func TestDeleteWarningAcrossConfirmationLayouts(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := driveRefresh(t, newModel("alice", []string{"ubuntu"}, 0, client, 120, 24))
	m = press(t, m, key('d'))
	const warning = "Local disk data is destroyed; data on retained PVCs is kept."
	for _, width := range []int{80, 200} {
		for _, height := range []int{24, 10, 6, 5, 4, 3, 2} {
			m.width, m.height = width, height
			if text := view(m); !strings.Contains(text, warning) {
				t.Errorf("%dx%d lacks warning:\n%s", width, height, text)
			}
		}
	}
}

func TestDialogCancelSemantics(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)

	for _, cancel := range []tea.KeyPressMsg{
		key('n'),
		{Code: tea.KeyEscape},
		key('q'),
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		m = press(t, m, key('d'))
		if !strings.Contains(view(m), "Delete box") {
			t.Fatalf("dialog did not open for %v", cancel)
		}
		m = press(t, m, cancel)
		if len(client.deleted) != 0 {
			t.Fatalf("cancel %v deleted the box", cancel)
		}
		if strings.Contains(view(m), "Delete box") {
			t.Fatalf("cancel %v left the dialog open", cancel)
		}
	}
	// q from the list view quits; from a dialog it only cancels.
	m = press(t, m, key('d'))
	next, cmd := m.Update(key('q'))
	if cmd != nil {
		t.Fatal("q inside the dialog returned a command")
	}
	m = next.(settingsModel)
	_, cmd = m.Update(key('q'))
	if cmd == nil {
		t.Fatal("q from the list view did not quit")
	}
}

func TestDeleteRequiresLiveConnectionAndExistingBox(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	// Unreachable: delete is refused with an explanatory message.
	client.failures = map[string]error{name: errors.New("connection refused")}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	m = press(t, m, key('d'))
	if m.dialog != dialogNone || len(client.deleted) != 0 {
		t.Fatal("delete was offered on an unreachable connection")
	}
	if !strings.Contains(view(m), "delete is unavailable") {
		t.Fatalf("no explanation for the disabled delete:\n%s", view(m))
	}

	// Live, but the selected box was never created.
	delete(client.failures, name)
	delete(client.pods, name)
	m = driveRefresh(t, m)
	m = press(t, m, key('d'))
	if m.dialog != dialogNone || len(client.deleted) != 0 {
		t.Fatal("delete was offered for a not-created box")
	}
	if !strings.Contains(view(m), "is not created") {
		t.Fatalf("no hint how to create the box:\n%s", view(m))
	}
}

func TestDeleteConfirmationRejectsStateThatBecameStale(t *testing.T) {
	for _, failure := range []error{errors.New("connection refused"), errForbidden} {
		for _, allFailed := range []bool{false, true} {
			name := boxPodName("alice", "ubuntu")
			other := boxPodName("alice", "data-engine")
			client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
			m := driveRefresh(t, newModel("alice", []string{"ubuntu", "data-engine"}, 0, client, 120, 24))
			m = press(t, m, key('d'))
			if m.dialog != dialogConfirmDelete {
				t.Fatal("confirmation did not open while Live")
			}
			client.failures = map[string]error{name: failure}
			if allFailed {
				client.failures[other] = failure
			}
			m = driveRefresh(t, m)
			next, cmd := m.Update(key('y'))
			m = next.(settingsModel)
			if cmd != nil || len(client.deleted) != 0 || m.dialog != dialogNone {
				t.Fatalf("stale confirmation issued delete: failure %v, all failed %v", failure, allFailed)
			}
			if !strings.Contains(m.message, "delete cancelled: the connection is "+m.conn.String()) {
				t.Fatalf("stale cancellation not explained: %q", m.message)
			}
		}
	}
}

func TestEmptyTemplateListRendersEmptyState(t *testing.T) {
	m := newModel("alice", nil, 0, &fakeClient{}, 120, 24)
	m = driveRefresh(t, m) // zero rows: still Live
	if m.conn != connLive {
		t.Fatalf("connection = %v, want Live with no templates", m.conn)
	}
	text := view(m)
	if !strings.Contains(text, "No box templates are available.") {
		t.Fatalf("empty state missing:\n%s", text)
	}
	m = press(t, m, key('d'))
	if m.dialog != dialogNone || !strings.Contains(view(m), "nothing to delete") {
		t.Fatalf("delete on the empty state:\n%s", view(m))
	}
}

func TestDeleteFailuresAreReportedNotSwallowed(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	m = press(t, m, key('d'))
	client.delErr = errForbidden
	m = confirmPress(t, m)
	if len(client.deleted) != 1 {
		t.Fatal("delete was not issued")
	}
	if m.dialog != dialogNone || !strings.Contains(view(m), "reconnect settings") {
		t.Fatalf("forbidden delete outcome not explained:\n%s", view(m))
	}
}

func TestPlainRenderingWithoutColors(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, 0, client, 120, 24)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "Running") {
		t.Fatalf("plain rendering lost the row:\n%s", view(m))
	}
	m = press(t, m, key('d'))
	text := view(m)
	for _, forbidden := range []string{sgrGreen, sgrRed, sgrYellow, sgrDim, sgrBold} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("NO_COLOR output still contains %q", forbidden)
		}
	}
	if !strings.Contains(text, "Delete box") {
		t.Fatalf("plain rendering lost the dialog:\n%s", text)
	}
}

func TestTinyTerminalsKeepTheFooter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	for _, size := range [][2]int{{80, 4}, {40, 3}, {20, 2}, {12, 1}} {
		m := newModel("alice", []string{"ubuntu"}, 0, client, size[0], size[1])
		m = driveRefresh(t, m)
		m = press(t, m, key('d'))
		text := view(m)
		if !strings.Contains(text, "q: quit") {
			t.Fatalf("%dx%d lost the quit hint:\n%s", size[0], size[1], text)
		}
		lines := strings.Split(text, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d rendered %d lines:\n%s", size[0], size[1], len(lines), text)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > max(1, size[0]) {
				t.Fatalf("%dx%d line exceeds width: %q", size[0], size[1], line)
			}
		}
	}
}

func TestParseMaxBoxes(t *testing.T) {
	for value, want := range map[string]int{"": 0, "0": 0, "3": 3, " 12 ": 12} {
		got, err := parseMaxBoxes(value)
		if err != nil || got != want {
			t.Fatalf("parseMaxBoxes(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, bad := range []string{"three", "-1", "1.5"} {
		if _, err := parseMaxBoxes(bad); err == nil {
			t.Fatalf("parseMaxBoxes(%q) accepted a malformed cap", bad)
		}
	}
}

func TestCapLineCountsExistingBoxes(t *testing.T) {
	ubuntu := boxPodName("alice", "ubuntu")
	data := boxPodName("alice", "data-engine")
	client := &fakeClient{pods: map[string]*podInfo{ubuntu: runningPod(ubuntu)}}
	m := newModel("alice", []string{"ubuntu", "data-engine"}, 2, client, 120, 24)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "created 1/2") {
		t.Fatalf("cap line missing:\n%s", view(m))
	}
	// At the cap the line names the way out.
	client.pods[data] = runningPod(data)
	m = driveRefresh(t, m)
	text := view(m)
	if !strings.Contains(text, "created 2/2") || !strings.Contains(text, "cap reached") {
		t.Fatalf("at-cap line lost its hint:\n%s", text)
	}
	// Terminating pods still exist and still count toward the cap.
	client.pods[ubuntu] = terminatingPod(ubuntu)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "created 2/2") {
		t.Fatalf("terminating box stopped counting:\n%s", view(m))
	}
	// A gone box releases budget.
	delete(client.pods, ubuntu)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "created 1/2") {
		t.Fatalf("gone box still counted:\n%s", view(m))
	}
	// maxBoxes 0 disables the element entirely.
	m0 := newModel("alice", []string{"ubuntu", "data-engine"}, 0, client, 120, 24)
	m0 = driveRefresh(t, m0)
	if strings.ContainsAny(view(m0), "█░") {
		t.Fatalf("cap element rendered with no cap set:\n%s", view(m0))
	}
	// Rows that never saw their pod (authorization refused) cannot vouch for
	// existence and do not count toward the cap.
	forbidden := &fakeClient{pods: map[string]*podInfo{}, failures: map[string]error{
		ubuntu: errForbidden, data: errForbidden,
	}}
	mf := newModel("alice", []string{"ubuntu", "data-engine"}, 2, forbidden, 120, 24)
	mf = driveRefresh(t, mf)
	if !strings.Contains(view(mf), "created 0/2") {
		t.Fatalf("never-seen rows counted toward the cap:\n%s", view(mf))
	}
}

// TestPanesShareHeight pins the master and detail panes to the taller pane's
// height: both boxes close on the same line, and the render never exceeds the
// terminal height.
func TestPanesShareHeight(t *testing.T) {
	// bottoms returns the indexes of all lines containing a pane bottom
	// border, and how many borders they carry.
	bottoms := func(text string) ([]int, int) {
		var lines []int
		count := 0
		for i, line := range strings.Split(text, "\n") {
			if n := strings.Count(line, "└"); n > 0 {
				lines = append(lines, i)
				count += n
			}
		}
		return lines, count
	}
	for _, tc := range []struct {
		name          string
		width, height int
		templates     []string
		runningPods   []string // templates with a running pod
	}{
		// A data detail pane (strip + ledger) is the taller pane: the master
		// grows blank rows above its cap separator.
		{"detail taller", 120, 24, []string{"ubuntu", "data-engine"}, []string{"ubuntu"}},
		// A state-only detail pane is the shorter one: the detail grows blank
		// rows above its bottom border.
		{"master taller", 120, 24, []string{"ubuntu", "data-lab", "tumbleweed", "kata-smoke"}, nil},
		// A scrolled master pane is the taller one at short heights.
		{"scrolled master", 120, 14, []string{"a", "b", "c", "d", "e", "f", "g", "h"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{pods: map[string]*podInfo{}}
			for _, template := range tc.runningPods {
				name := boxPodName("alice", template)
				client.pods[name] = runningPod(name)
			}
			m := newModel("alice", tc.templates, 3, client, tc.width, tc.height)
			m = driveRefresh(t, m)
			text := view(m)
			lines := strings.Split(text, "\n")
			if len(lines) != tc.height {
				t.Fatalf("rendered %d lines for a %d-row terminal:\n%s", len(lines), tc.height, text)
			}
			bottom, count := bottoms(text)
			// Detail needs width for the strip; narrow cases are master-only.
			if tc.width >= 100 {
				if len(bottom) != 1 || count != 2 {
					t.Fatalf("panes do not close on one shared line: bottoms %v (borders %d):\n%s", bottom, count, text)
				}
			} else if len(bottom) != 1 || count != 1 {
				t.Fatalf("master pane bottom not on one line: bottoms %v (borders %d):\n%s", bottom, count, text)
			}
		})
	}

	// The cap line stays footed: it is the last content line of the master
	// pane, directly above the shared bottom border.
	ubuntu := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{ubuntu: runningPod(ubuntu)}}
	m := newModel("alice", []string{"ubuntu", "data-engine"}, 3, client, 120, 24)
	m = driveRefresh(t, m)
	text := view(m)
	lines := strings.Split(text, "\n")
	bottom, _ := bottoms(text)
	if len(bottom) != 1 || !strings.Contains(lines[bottom[0]-1], "created 1/3") {
		t.Fatalf("cap line not footed above the pane bottom:\n%s", text)
	}

	// The detail pane fills the width left of the master pane and keeps its
	// anchors: the status strip is tight at the top with the separator right
	// below it, the ledger rows stay footed above the shared bottom line, and
	// the pane pair spans the terminal width.
	m = newModel("alice", []string{"ubuntu"}, 3, client, 120, 24)
	m = driveRefresh(t, m)
	text = view(m)
	lines = strings.Split(text, "\n")
	bottom, _ = bottoms(text)
	if len(bottom) != 1 {
		t.Fatalf("panes do not close on one shared line:\n%s", text)
	}
	strip := -1
	for i, line := range lines {
		if strings.Contains(line, "RESTARTS") {
			strip = i
			break
		}
	}
	if strip < 1 || !strings.Contains(lines[strip+1], "up 2h") || !strings.Contains(lines[strip+2], "├") {
		t.Fatalf("status strip not tight at the top with its separator below:\n%s", text)
	}
	ledger := lines[bottom[0]-4 : bottom[0]]
	for i, label := range []string{"IMAGE", "RUNTIME", "REQUESTS", "LIMITS"} {
		if !strings.Contains(ledger[i], label) {
			t.Fatalf("ledger rows not footed above the pane bottom:\n%s", text)
		}
	}
	for i, line := range lines[:bottom[0]+1] {
		if w := ansi.StringWidth(line); w != 120 {
			t.Fatalf("pane line %d spans %d columns, want 120:\n%s", i, w, text)
		}
	}

	// The honesty line still drops when the pane does not fit: it needs one
	// body line more than the strip+ledger pane, so a 10-row terminal drops it
	// while an 11-row one keeps it — both fill their height exactly.
	m = newModel("alice", []string{"ubuntu"}, 3, client, 120, 10)
	m = driveRefresh(t, m) // the row carries live data
	client.failures = map[string]error{ubuntu: errors.New("connection refused")}
	m = driveRefresh(t, m) // stale row: last-known data plus honesty line
	text = view(m)
	if n := len(strings.Split(text, "\n")); n != 10 {
		t.Fatalf("stale detail at 120x10 rendered %d lines:\n%s", n, text)
	}
	if strings.Contains(text, honestyLastKnown) {
		t.Fatalf("honesty line rendered without room at 120x10:\n%s", text)
	}
	client.failures = nil
	m = newModel("alice", []string{"ubuntu"}, 3, client, 120, 11)
	m = driveRefresh(t, m) // the row carries live data
	client.failures = map[string]error{ubuntu: errors.New("connection refused")}
	m = driveRefresh(t, m) // stale row: last-known data plus honesty line
	text = view(m)
	if n := len(strings.Split(text, "\n")); n != 11 {
		t.Fatalf("stale detail at 120x11 rendered %d lines:\n%s", n, text)
	}
	if !strings.Contains(text, honestyLastKnown) {
		t.Fatalf("stale detail lost its honesty line at 120x11:\n%s", text)
	}
}
