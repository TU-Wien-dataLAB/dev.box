package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.containerssh.io/containerssh/config"
)

func TestUnmatchedTemplateDenied(t *testing.T) {
	for _, mode := range []config.KubernetesExecutionMode{
		config.KubernetesExecutionModeConnection,
		config.KubernetesExecutionModeSession,
		config.KubernetesExecutionModePersistent,
	} {
		for _, withDefault := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/withDefault=%t", mode, withDefault), func(t *testing.T) {
				files := map[string]string{"ubuntu.yaml": "{}", "bad_name.yaml": "{}"}
				if withDefault {
					files["default.yaml"] = "{}"
					files["default.json"] = "{}"
				}
				h := testHandler(t, func(h *configHandler) {
					h.dir = testTemplateDir(t, files)
					h.boxes.operatingMode = mode
					h.boxes.pods = &fakeLister{names: func(context.Context, string, string) ([]string, error) {
						t.Fatal("unmatched template must be denied before listing pods")
						return nil, nil
					}}
				})
				for _, username := range []string{"unknown", "", " ", "../ubuntu", " ubuntu", "ubuntu ", "bad/name", "bad name"} {
					cfg, err := h.OnConfig(configRequest(username, "alice"))
					if !errors.Is(err, errNoMatchingTemplate) {
						t.Fatalf("username %q: error = %v, want no matching template", username, err)
					}
					if !reflect.DeepEqual(cfg, config.AppConfig{}) {
						t.Fatalf("username %q: denied response contains config", username)
					}
				}
			})
		}
	}
}

func TestNamedTemplatesStillServed(t *testing.T) {
	for _, filename := range []string{"ubuntu.yaml", "ubuntu.json", "default.yaml", "default.json"} {
		t.Run(filename, func(t *testing.T) {
			h := testHandler(t, func(h *configHandler) {
				// JSON is also valid YAML, so both extensions use the same parser.
				h.dir = testTemplateDir(t, map[string]string{filename: `{"kubernetes":{"pod":{"metadata":{"labels":{"template":"selected"}}}}}`})
			})
			username := filename[:len(filename)-len(filepath.Ext(filename))]
			cfg, err := h.OnConfig(configRequest(username, "alice"))
			if err != nil {
				t.Fatalf("named template: %v", err)
			}
			if cfg.Kubernetes.Pod.Metadata.Labels["template"] != "selected" || cfg.Kubernetes.Pod.Metadata.Name == "" {
				t.Fatal("named template was not served with persistent injection")
			}
		})
	}
}

func TestMalformedTemplateDoesNotFallBack(t *testing.T) {
	h := testHandler(t, func(h *configHandler) {
		h.dir = testTemplateDir(t, map[string]string{
			"ubuntu.yaml":  "kubernetes: [",
			"ubuntu.json":  "{}",
			"default.yaml": "{}",
		})
	})
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil || errors.Is(err, errNoMatchingTemplate) {
		t.Fatalf("malformed template: error = %v, want parse error", err)
	}
}

func TestRemovedCachedTemplateDenied(t *testing.T) {
	h := testHandler(t)
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err != nil {
		t.Fatalf("initial request: %v", err)
	}
	if err := os.Remove(filepath.Join(h.dir, "ubuntu.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); !errors.Is(err, errNoMatchingTemplate) {
		t.Fatalf("removed cached template: error = %v, want no matching template", err)
	}
}
