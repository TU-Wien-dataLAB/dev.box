package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/metadata"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeLister is the in-process stand-in for the single new seam: pod listing
// by owner label. Tests drive both the happy path and the fail-closed error.
type fakeLister struct {
	// names returns the pod names for a label; override to simulate failures.
	names func(ctx context.Context, namespace, ownerLabel string) ([]string, error)
}

func (f *fakeLister) LivePodNamesForOwner(ctx context.Context, namespace, ownerLabel string) ([]string, error) {
	if f.names == nil {
		return nil, nil
	}
	return f.names(ctx, namespace, ownerLabel)
}

func testTemplateDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write template %s: %v", name, err)
		}
	}
	return dir
}

// testHandler builds a persistent-mode handler with a fake, empty lister.
// opts override the template directory or persistent-box settings.
func testHandler(t *testing.T, opts ...func(*configHandler)) *configHandler {
	t.Helper()
	logger, err := log.NewLogger(config.LogConfig{
		Level:       config.LogLevelCritical,
		Format:      config.LogFormatLJSON,
		Destination: config.LogDestinationStdout,
	})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	h := &configHandler{
		dir:    testTemplateDir(t, nil),
		logger: logger,
		cache:  map[string]cachedEntry{},
		boxes: persistentBoxConfig{
			operatingMode: config.KubernetesExecutionModePersistent,
			namespace:     "containerssh-sessions",
			maxPods:       3,
			pods:          &fakeLister{},
		},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// configRequest builds a config webhook request: login is the SSH username the
// client typed, authenticated is the verified identity (authenticatedUsername).
func configRequest(login, authenticated string) config.Request {
	meta := metadata.NewTestAuthenticatingMetadata(login).Authenticated(authenticated)
	return config.Request{ConnectionAuthenticatedMetadata: meta}
}

func TestDeterministicPodNaming(t *testing.T) {
	dir := testTemplateDir(t, map[string]string{
		"ubuntu.yaml": "kubernetes:\n  pod:\n    metadata:\n      labels:\n        template: ubuntu\n",
		"dev.yaml":    "kubernetes:\n  pod:\n    metadata:\n      labels:\n        template: dev\n",
	})
	h := testHandler(t, func(h *configHandler) { h.dir = dir })

	first, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if first.Kubernetes.Pod.Metadata.Name != second.Kubernetes.Pod.Metadata.Name {
		t.Fatalf("same (owner, template) produced different names: %s vs %s",
			first.Kubernetes.Pod.Metadata.Name, second.Kubernetes.Pod.Metadata.Name)
	}

	otherUser, err := h.OnConfig(configRequest("ubuntu", "bob"))
	if err != nil {
		t.Fatalf("other user: %v", err)
	}
	if otherUser.Kubernetes.Pod.Metadata.Name == first.Kubernetes.Pod.Metadata.Name {
		t.Fatalf("different owners got the same pod name %s", first.Kubernetes.Pod.Metadata.Name)
	}

	otherTemplate, err := h.OnConfig(configRequest("dev", "alice"))
	if err != nil {
		t.Fatalf("other template: %v", err)
	}
	if otherTemplate.Kubernetes.Pod.Metadata.Name == first.Kubernetes.Pod.Metadata.Name {
		t.Fatalf("different templates of one owner got the same pod name %s", first.Kubernetes.Pod.Metadata.Name)
	}
}

func TestDefaultTemplateFallbackCollapse(t *testing.T) {
	// No default.yaml on purpose: both unknown logins resolve to the same
	// "default" box for one owner, while a different owner gets its own default box.
	h := testHandler(t)

	one, err := h.OnConfig(configRequest("nobody-1", "alice"))
	if err != nil {
		t.Fatalf("nobody-1: %v", err)
	}
	if one.Kubernetes.Pod.Metadata.Name == "" {
		t.Fatal("unknown username got no pod name")
	}
	two, err := h.OnConfig(configRequest("nobody-2", "alice"))
	if err != nil {
		t.Fatalf("nobody-2: %v", err)
	}
	if two.Kubernetes.Pod.Metadata.Name != one.Kubernetes.Pod.Metadata.Name {
		t.Fatalf("unknown logins of one owner should collapse onto the same default box: %s vs %s",
			one.Kubernetes.Pod.Metadata.Name, two.Kubernetes.Pod.Metadata.Name)
	}

	other, err := h.OnConfig(configRequest("nobody-1", "bob"))
	if err != nil {
		t.Fatalf("other owner as nobody-1: %v", err)
	}
	if other.Kubernetes.Pod.Metadata.Name == one.Kubernetes.Pod.Metadata.Name {
		t.Fatalf("two owners must never share a default box: %s", one.Kubernetes.Pod.Metadata.Name)
	}
}

func TestOwnerLabelInjected(t *testing.T) {
	h := testHandler(t)

	cfg, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	name := cfg.Kubernetes.Pod.Metadata.Name
	if !strings.HasPrefix(name, boxNamePrefix) {
		t.Fatalf("pod name %q does not carry the %q prefix", name, boxNamePrefix)
	}
	if len(name) > 63 {
		t.Fatalf("pod name %q exceeds 63 chars", name)
	}
	if owner := cfg.Kubernetes.Pod.Metadata.Labels[ownerLabelKey]; owner != "alice" {
		t.Fatalf("owner label = %q, want alice", owner)
	}
	if !cfg.Kubernetes.Pod.CreateMissingPods {
		t.Fatal("persistent response did not enable creation of the named pod")
	}
}

func TestOwnerLabelsAreIsolatedAcrossCachedTemplateRequests(t *testing.T) {
	dir := testTemplateDir(t, map[string]string{
		"ubuntu.yaml": "kubernetes:\n  pod:\n    metadata:\n      labels:\n        template: ubuntu\n",
	})
	h := testHandler(t, func(h *configHandler) { h.dir = dir })

	alice, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatalf("alice request: %v", err)
	}
	bob, err := h.OnConfig(configRequest("ubuntu", "bob"))
	if err != nil {
		t.Fatalf("bob request: %v", err)
	}

	if owner := alice.Kubernetes.Pod.Metadata.Labels[ownerLabelKey]; owner != "alice" {
		t.Fatalf("alice response owner label changed to %q after bob's request", owner)
	}
	if owner := bob.Kubernetes.Pod.Metadata.Labels[ownerLabelKey]; owner != "bob" {
		t.Fatalf("bob response owner label = %q, want bob", owner)
	}
	for _, cfg := range []config.AppConfig{alice, bob} {
		if label := cfg.Kubernetes.Pod.Metadata.Labels["template"]; label != "ubuntu" {
			t.Fatalf("template label = %q, want ubuntu", label)
		}
	}
}

func TestInjectionSkipsNonPersistentTemplate(t *testing.T) {
	dir := testTemplateDir(t, map[string]string{
		"ubuntu.yaml": "kubernetes:\n  pod:\n    mode: connection\n    metadata:\n      labels:\n        template: ubuntu\n",
	})
	h := testHandler(t, func(h *configHandler) { h.dir = dir })

	cfg, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if cfg.Kubernetes.Pod.Metadata.Name != "" {
		t.Fatalf("non-persistent template got a fixed name %q", cfg.Kubernetes.Pod.Metadata.Name)
	}
	if _, ok := cfg.Kubernetes.Pod.Metadata.Labels[ownerLabelKey]; ok {
		t.Fatal("non-persistent template got an owner label")
	}
	if cfg.Kubernetes.Pod.CreateMissingPods {
		t.Fatal("non-persistent template enabled persistent pod creation")
	}
}

func TestEmptyAuthenticatedIdentityDenied(t *testing.T) {
	h := testHandler(t)

	if _, err := h.OnConfig(configRequest("ubuntu", "")); err == nil {
		t.Fatal("empty authenticated identity was not denied")
	}
}

func TestCapEnforcement(t *testing.T) {
	t.Run("below cap allows a new box", func(t *testing.T) {
		h := testHandler(t, func(h *configHandler) {
			h.boxes.maxPods = 2
			h.boxes.pods = &fakeLister{names: func(_ context.Context, _, _ string) ([]string, error) {
				return []string{"box-aaaaaaaaaa"}, nil
			}}
		})
		cfg, err := h.OnConfig(configRequest("ubuntu", "alice"))
		if err != nil {
			t.Fatalf("new box under the cap was denied: %v", err)
		}
		if cfg.Kubernetes.Pod.Metadata.Name == "" {
			t.Fatal("allowed request got no pod name")
		}
	})

	t.Run("at cap denies a new box", func(t *testing.T) {
		h := testHandler(t, func(h *configHandler) {
			h.boxes.maxPods = 2
			h.boxes.pods = &fakeLister{names: func(_ context.Context, _, _ string) ([]string, error) {
				return []string{"box-aaaaaaaaaa", "box-bbbbbbbbbb"}, nil
			}}
		})
		if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
			t.Fatal("new box at the cap was not denied")
		}
	})

	t.Run("reconnect at cap always passes", func(t *testing.T) {
		dir := testTemplateDir(t, map[string]string{
			"ubuntu.yaml": "kubernetes:\n  pod:\n    metadata:\n      labels:\n        template: ubuntu\n",
		})
		probe := testHandler(t, func(h *configHandler) { h.dir = dir })
		req := configRequest("ubuntu", "alice")
		probed, err := probe.OnConfig(req)
		if err != nil {
			t.Fatalf("probe request: %v", err)
		}
		existing := probed.Kubernetes.Pod.Metadata.Name
		if existing == "" {
			t.Fatal("probe request returned no pod name")
		}
		h := testHandler(t, func(h *configHandler) {
			h.dir = dir
			h.boxes.maxPods = 1
			h.boxes.pods = &fakeLister{names: func(_ context.Context, _, _ string) ([]string, error) {
				return []string{existing}, nil
			}}
		})
		cfg, err := h.OnConfig(req)
		if err != nil {
			t.Fatalf("reconnect to an existing box at the cap was denied: %v", err)
		}
		if cfg.Kubernetes.Pod.Metadata.Name != existing {
			t.Fatalf("reconnect targeted %q, got %q", existing, cfg.Kubernetes.Pod.Metadata.Name)
		}
	})

	t.Run("listing failure denies (fail closed)", func(t *testing.T) {
		h := testHandler(t, func(h *configHandler) {
			h.boxes.pods = &fakeLister{names: func(_ context.Context, _, _ string) ([]string, error) {
				return nil, errors.New("kubernetes API unreachable")
			}}
		})
		if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
			t.Fatal("pod-listing failure was not denied")
		}
	})

	t.Run("cap disabled never lists", func(t *testing.T) {
		var lists int
		h := testHandler(t, func(h *configHandler) {
			h.boxes.maxPods = 0
			h.boxes.pods = &fakeLister{names: func(_ context.Context, _, _ string) ([]string, error) {
				lists++
				return nil, nil
			}}
		})
		if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err != nil {
			t.Fatalf("request with cap disabled failed: %v", err)
		}
		if lists != 0 {
			t.Fatalf("cap disabled still listed pods %d times", lists)
		}
	})
}

func TestKubePodListerReturnsOnlyLivePodNames(t *testing.T) {
	now := metav1.Now()
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}}, Status: corev1.PodStatus{Phase: corev1.PodUnknown}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "succeeded", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "terminating", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "alice"}, DeletionTimestamp: &now, Finalizers: []string{"test"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-owner", Namespace: "boxes", Labels: map[string]string{ownerLabelKey: "bob"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	)
	lister := &kubePodLister{client: client}

	names, err := lister.LivePodNamesForOwner(context.Background(), "boxes", "alice")
	if err != nil {
		t.Fatalf("list live pods: %v", err)
	}
	want := []string{"running", "pending", "unknown"}
	sort.Strings(names)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("live pod names = %v, want %v", names, want)
	}
}

func TestOwnerLabelValue(t *testing.T) {
	for _, owner := range []string{
		"alice", "matthias.matt", "slurm_tu_wien", "UPPER_case.Name-1",
	} {
		if got := ownerLabelValue(owner); got != owner {
			t.Fatalf("ownerLabelValue(%q) = %q, want verbatim", owner, got)
		}
		if !isValidLabelValue(owner) {
			t.Fatalf("test owner %q is not a valid label value", owner)
		}
	}

	// Lossy usernames get sanitized into valid, unique, deterministic labels.
	a := ownerLabelValue("has spaces here")
	if !isValidLabelValue(a) {
		t.Fatalf("sanitized label %q is not a valid label value", a)
	}
	if ownerLabelValue("has spaces here") != a {
		t.Fatal("sanitized label is not deterministic")
	}
	b := ownerLabelValue("has-spaces-here")
	if b != "has-spaces-here" {
		t.Fatalf("valid username unexpectedly sanitized: %q", b)
	}
	if a == b {
		t.Fatalf("lossy sanitization collided with a verbatim label: %q == %q", a, b)
	}
	if long := ownerLabelValue(strings.Repeat("u", 80)); len(long) > 63 {
		t.Fatalf("long username produced label of %d chars", len(long))
	}
}
