package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/config/webhook"
	"gopkg.in/yaml.v3"
)

// The optional output feeds this actual HTTP response to the real gateway
// merger/binary suite, instead of maintaining a second settings template fixture.
func TestSettingsWebhookResponse(t *testing.T) {
	h := settingsHandler(t)
	h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": "{}"})
	handler, err := webhook.NewHandler(h, h.logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"alice", "bob", "", " \t"} {
		request, err := json.Marshal(configRequest("settings", owner))
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/config", bytes.NewReader(request))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, req)
		if owner == "" || owner == " \t" {
			if recorder.Code == http.StatusOK {
				t.Fatal("empty verified identity received a config over HTTP")
			}
			continue
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("webhook HTTP %d: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Config config.AppConfig `json:"config"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		pod := response.Config.Kubernetes.Pod
		if pod.Mode != config.KubernetesExecutionModeConnection || pod.Spec.Containers[0].Env[0].Value != owner || pod.Spec.ServiceAccountName == "" {
			t.Fatalf("HTTP response lost settings identity: %+v", pod)
		}
		if path := os.Getenv("CONTAINERSSH_TEST_SETTINGS_RESPONSE"); owner == "alice" && path != "" {
			data, err := yaml.Marshal(response.Config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
