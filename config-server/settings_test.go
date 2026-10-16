package main

import (
	"context"
	"errors"
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
	var owners []string
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "DEV_BOX_AUTHENTICATED_OWNER" {
			owners = append(owners, env.Value)
		}
	}
	if len(owners) != 1 || owners[0] != "alice" {
		t.Fatalf("settings owner = %q, want only verified alice", owners)
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
