package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/config/webhook"
	"gopkg.in/yaml.v3"
)

// Optional input/output joins the real Helm template, HTTP response, v0.6 merger
// and gateway binary in chart-config-binary-validation.sh; no parallel fixture.
func TestStorageWebhookResponse(t *testing.T) {
	h := storageHandler(t)
	if path := os.Getenv("CONTAINERSSH_TEST_STORAGE_TEMPLATE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": string(data)})
	}
	handler, err := webhook.NewHandler(h, h.logger)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(configRequest("ubuntu", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", bytes.NewReader(request))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "volumeClaimTemplates") {
		t.Fatal("claim envelope leaked into AppConfig wire response")
	}
	var response struct {
		Config config.AppConfig `json:"config"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	pod := response.Config.Kubernetes.Pod
	if pod.Metadata.Name != "box-ec061c6832" || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "box-ec061c6832-home" {
		t.Fatalf("wire response lost per-box claim: %+v", pod)
	}
	if path := os.Getenv("CONTAINERSSH_TEST_STORAGE_RESPONSE"); path != "" {
		data, err := yaml.Marshal(response.Config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
