package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.containerssh.io/containerssh/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const settingsTemplate = `kubernetes:
  pod:
    mode: connection
    shellCommand: [/usr/local/bin/dev-box-settings]
    spec:
      automountServiceAccountToken: false
      containers:
        - name: settings
          image: settings:test
          env:
            - name: DEV_BOX_AUTHENTICATED_OWNER
              value: untrusted-template-value
security:
  defaultMode: disable
  forceCommand: /usr/local/bin/dev-box-settings
  shell:
    mode: enable
  tty:
    mode: enable
  maxSessions: 1
`

func settingsHandler(t *testing.T) *configHandler {
	t.Helper()
	return testHandler(t, func(h *configHandler) {
		h.dir = testTemplateDir(t, map[string]string{"settings.yaml": settingsTemplate, "ubuntu.yaml": "{}"})
		h.boxes.pods = &fakeLister{names: func(context.Context, string, string) ([]string, error) {
			return nil, errors.New("settings must not consult the persistent box cap")
		}}
	})
}

func TestSettingsIsSynthesizedWithoutATemplateFile(t *testing.T) {
	h := settingsHandler(t)
	h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": "{}"})
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.DefaultMode != config.ExecutionPolicyDisable || cfg.Security.MaxSessions != 1 || cfg.Security.ForceCommand != "/usr/local/bin/dev-box-settings" {
		t.Fatalf("settings security floor missing: %+v", cfg.Security)
	}
	pod := cfg.Kubernetes.Pod
	if pod.Spec.RuntimeClassName != nil || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "settings" {
		t.Fatalf("settings is not a single default-runtime application: %+v", pod.Spec)
	}
	container := pod.Spec.Containers[0]
	if container.SecurityContext == nil || !*container.SecurityContext.ReadOnlyRootFilesystem || *container.SecurityContext.AllowPrivilegeEscalation || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("settings container security floor missing: %+v", container.SecurityContext)
	}
}

func TestSettingsReceivesVerifiedOwnerWithoutOccupyingPersistentQuota(t *testing.T) {
	h := settingsHandler(t)
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	pod := cfg.Kubernetes.Pod
	if pod.Mode != config.KubernetesExecutionModeConnection || pod.CreateMissingPods || pod.Metadata.Name != "" || pod.Metadata.Labels[ownerLabelKey] != "" {
		t.Fatalf("settings became a persistent box: %+v", pod.Metadata)
	}
	env := pod.Spec.Containers[0].Env
	if len(env) != 2 || env[0].Name != "DEV_BOX_AUTHENTICATED_OWNER" || env[0].Value != "alice" ||
		env[1].Name != "DEV_BOX_BOX_TEMPLATES" || env[1].Value != "ubuntu" {
		t.Fatalf("settings env = %+v; want exactly the verified owner and box template list", env)
	}
}

func TestSettingsEnsuresOwnerScopedKubernetesAccess(t *testing.T) {
	h := settingsHandler(t)
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	spec := cfg.Kubernetes.Pod.Spec
	if spec.ServiceAccountName == "" || spec.AutomountServiceAccountToken == nil || !*spec.AutomountServiceAccountToken {
		t.Fatal("settings has no per-owner projected token")
	}
	ctx := context.Background()
	ns := h.boxes.namespace
	if _, err := h.kube.CoreV1().ServiceAccounts(ns).Get(ctx, spec.ServiceAccountName, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	role, err := h.kube.RbacV1().Roles(ns).Get(ctx, spec.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.boxes.pods = &fakeLister{}
	ordinary, err := h.OnConfig(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != ordinary.Kubernetes.Pod.Metadata.Name || strings.Join(role.Rules[0].Verbs, ",") != "get,delete" || strings.Join(role.Rules[0].Resources, ",") != "pods" || len(role.Rules[0].APIGroups) != 1 || role.Rules[0].APIGroups[0] != "" {
		t.Fatalf("unexpected owner authority: %+v", role.Rules)
	}
	binding, err := h.kube.RbacV1().RoleBindings(ns).Get(ctx, spec.ServiceAccountName, metav1.GetOptions{})
	if err != nil || binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 || binding.Subjects[0].Name != spec.ServiceAccountName || binding.Subjects[0].Namespace != ns || binding.Subjects[0].Kind != "ServiceAccount" {
		t.Fatalf("unexpected owner binding: %+v, %v", binding, err)
	}
}

func TestSettingsMissingVerifiedOwnerDenies(t *testing.T) {
	h := settingsHandler(t)
	for _, owner := range []string{"", "  \t"} {
		if _, err := h.OnConfig(configRequest("settings", owner)); !errors.Is(err, errEmptyAuthenticatedIdentity) {
			t.Fatalf("owner %q: error = %v, want missing identity denial", owner, err)
		}
	}
}

func TestSettingsOwnersAreIsolatedAcrossConcurrentConnections(t *testing.T) {
	h := settingsHandler(t)
	var wg sync.WaitGroup
	for _, owner := range []string{"alice", "bob", "alice", "bob"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			cfg, err := h.OnConfig(configRequest("settings", owner))
			if err != nil {
				t.Error(err)
				return
			}
			for _, env := range cfg.Kubernetes.Pod.Spec.Containers[0].Env {
				if env.Name == "DEV_BOX_AUTHENTICATED_OWNER" && env.Value != owner {
					t.Errorf("owner %q received %q", owner, env.Value)
				}
			}
		}(owner)
	}
	wg.Wait()
}

// The management UI's row list is the owner Role's template list: only
// ordinary persistent templates, sorted, joined; nothing derived from the
// client. Non-persistent templates, settings and invalid names never appear.
func TestSettingsTemplateListIsThePersistentBoxSurface(t *testing.T) {
	h := settingsHandler(t)
	h.dir = testTemplateDir(t, map[string]string{
		"zeta.yaml":       "{}",
		"ubuntu.yaml":     "{}",
		"alpha.json":      "{}",
		"ephemeral.yaml":  "kubernetes:\n  pod:\n    mode: connection\n",
		"settings.yaml":   "{}",
		"ignored.txt":     "{}",
		"bad name.yaml":   "{}",
	})
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	env := cfg.Kubernetes.Pod.Spec.Containers[0].Env
	if len(env) != 2 || env[1].Name != "DEV_BOX_BOX_TEMPLATES" || env[1].Value != "alpha,ubuntu,zeta" {
		t.Fatalf("settings template list = %+v, want sorted persistent ordinary templates", env)
	}
	// The Role grants get/delete on exactly those templates' deterministic names.
	role, err := h.kube.RbacV1().Roles(h.boxes.namespace).Get(
		context.Background(), cfg.Kubernetes.Pod.Spec.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, template := range []string{"alpha", "ubuntu", "zeta"} {
		want = append(want, boxPodName("alice", template))
	}
	sort.Strings(want)
	if len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0].ResourceNames, want) {
		t.Fatalf("Role names %v diverged from template list %v", role.Rules[0].ResourceNames, want)
	}
}

// An empty list (connection mode or no persistent templates) is still served:
// the management UI renders its explicit empty state and the Role has no rules.
func TestSettingsEmptyTemplateListStillServes(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(*configHandler)
	}{{
		name: "no templates",
		prep: func(h *configHandler) { h.dir = testTemplateDir(t, nil) },
	}, {
		name: "connection mode",
		prep: func(h *configHandler) {
			h.boxes.operatingMode = config.KubernetesExecutionModeConnection
			h.boxes.pods = nil
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			h := settingsHandler(t)
			tc.prep(h)
			cfg, err := h.OnConfig(configRequest("settings", "alice"))
			if err != nil {
				t.Fatal(err)
			}
		env := cfg.Kubernetes.Pod.Spec.Containers[0].Env
			if len(env) != 2 || env[1].Value != "" {
				t.Fatalf("empty template list = %+v, want a present-but-empty trusted list", env)
			}
			role, err := h.kube.RbacV1().Roles(h.boxes.namespace).Get(
				context.Background(), cfg.Kubernetes.Pod.Spec.ServiceAccountName, metav1.GetOptions{})
			if err != nil || len(role.Rules) != 0 {
				t.Fatalf("empty template list granted rules: %+v, %v", role, err)
			}
		})
	}
}

// Unreadable/unparsable template files deny the settings response instead of
// serving a partial row list or stale grants.
func TestSettingsUnreadableTemplateListDenies(t *testing.T) {
	h := settingsHandler(t)
	h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": "{}", "broken.yaml": "kubernetes: ["})
	if _, err := h.OnConfig(configRequest("settings", "alice")); err == nil {
		t.Fatal("broken template file did not deny settings")
	}
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
		t.Fatal("broken template file still served an ordinary template")
	}
}

func TestSettingsCannotBeOverriddenByATemplateFile(t *testing.T) {
	for _, template := range []string{"{}", strings.Replace(settingsTemplate, "mode: connection", "mode: persistent", 1), "kubernetes:\n  pod:\n    mode: connection\n"} {
		h := settingsHandler(t)
		h.dir = testTemplateDir(t, map[string]string{"settings.yaml": template})
		cfg, err := h.OnConfig(configRequest("settings", "alice"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Kubernetes.Pod.Mode != config.KubernetesExecutionModeConnection || cfg.Kubernetes.Pod.Spec.Containers[0].Image != "settings:test" {
			t.Fatal("template file overrode reserved application")
		}
	}
}
