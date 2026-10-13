package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.containerssh.io/containerssh/config"
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

func TestSettingsRejectsPersistentOrIncompleteApplicationTemplate(t *testing.T) {
	for _, template := range []string{"{}", strings.Replace(settingsTemplate, "mode: connection", "mode: persistent", 1), "kubernetes:\n  pod:\n    mode: connection\n"} {
		h := settingsHandler(t)
		h.dir = testTemplateDir(t, map[string]string{"settings.yaml": template})
		if _, err := h.OnConfig(configRequest("settings", "alice")); err == nil {
			t.Fatal("unsafe settings template was accepted")
		}
	}
}
