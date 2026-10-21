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
				"resources": {"limits": {"cpu": "500m", "memory": "1Gi"}}}]
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
	m := newModel("alice", []string{"ubuntu", "data-engine"}, client, 120, 24)
	m = driveRefresh(t, m)
	if m.conn != connLive {
		t.Fatalf("connection = %v, want Live", m.conn)
	}
	text := view(m)
	for _, fragment := range []string{
		"Owner: alice",
		"Connection: Live",
		"ubuntu",
		"Running",
		"up 2h",
		"containerssh/containerssh-guest-image",
		"kata-qemu-runtime-rs",
		"restarts 1",
		"1Gi/500m",
		"data-engine",
		"not created · ssh data-engine@" + sshHostPlaceholder,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("row render missing %q in:\n%s", fragment, text)
		}
	}
	if strings.Count(text, "▸") != 1 {
		t.Fatalf("selection marker count = %d", strings.Count(text, "▸"))
	}
}

func TestRefreshKeepsLastKnownRowsAndReportsHonestStatus(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu", "data-engine"}, client, 120, 24)
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
	if !strings.Contains(view(m), "Running (last known)") {
		t.Fatalf("last-known row lost:\n%s", view(m))
	}
	// Partial: one transport failure, one resolved not-created row.
	delete(client.failures, boxPodName("alice", "data-engine"))
	m = driveRefresh(t, m)
	if m.conn != connPartial {
		t.Fatalf("connection = %v, want Partial", m.conn)
	}
	if !strings.Contains(view(m), "Connection: Partial") {
		t.Fatalf("partial status not shown:\n%s", view(m))
	}
}

func TestForbiddenRowShowsReconnectHint(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{}, failures: map[string]error{name: errForbidden}}
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
	m = driveRefresh(t, m)
	if m.conn != connUnreachable {
		t.Fatalf("connection = %v", m.conn)
	}
	if !strings.Contains(view(m), "reconnect") {
		t.Fatalf("reconnect hint missing:\n%s", view(m))
	}
	// Mid-session authorization change on a row with last-known data: the
	// reconnect hint must win over the generic last-known suffix.
	delete(client.failures, name)
	client.pods[name] = runningPod(name)
	m = driveRefresh(t, m)
	client.failures = map[string]error{name: errForbidden}
	m = driveRefresh(t, m)
	text := view(m)
	if !strings.Contains(text, "Running (reconnect)") {
		t.Fatalf("mid-session forbidden row lost the reconnect hint:\n%s", text)
	}
	if !strings.Contains(text, "containerssh/containerssh-guest-image") {
		t.Fatalf("last-known row data lost:\n%s", text)
	}
}

func TestTerminatingTransitionAfterDelete(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
	m = driveRefresh(t, m)

	// d opens the confirmation; y confirms; the delete completes and an
	// immediate refresh shows the deleting pod as Terminating.
	m = press(t, m, key('d'))
	if !strings.Contains(view(m), "Delete box \"ubuntu\" ("+name+")?") {
		t.Fatalf("confirmation missing:\n%s", view(m))
	}
	for _, consequence := range []string{
		"all data on the box's local disk is destroyed",
		"active sessions in the box are killed",
		"a fresh box is created on your next login: ssh ubuntu@" + sshHostPlaceholder,
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
	// Once the pod is gone, the row settles into the dimmed not-created form.
	delete(client.pods, name)
	m = driveRefresh(t, m)
	if !strings.Contains(view(m), "not created · ssh ubuntu@"+sshHostPlaceholder) {
		t.Fatalf("post-delete row not dimmed:\n%s", view(m))
	}
}

func TestDialogCancelSemantics(t *testing.T) {
	name := boxPodName("alice", "ubuntu")
	client := &fakeClient{pods: map[string]*podInfo{name: runningPod(name)}}
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
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
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
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

func TestEmptyTemplateListRendersEmptyState(t *testing.T) {
	m := newModel("alice", nil, &fakeClient{}, 120, 24)
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
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
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
	m := newModel("alice", []string{"ubuntu"}, client, 120, 24)
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
		m := newModel("alice", []string{"ubuntu"}, client, size[0], size[1])
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
