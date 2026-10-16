package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authWebhook "go.containerssh.io/containerssh/auth/webhook"
	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/metadata"
)

func TestWebhookDecisionMessageBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision authDecision
		err      error
		status   int
		message  string
	}{
		{"denial", authDecision{message: "denied\n"}, nil, http.StatusOK, "denied\n"},
		{"success suppresses message", authDecision{success: true, message: "must not leak"}, nil, http.StatusOK, ""},
		{"error suppresses success and message", authDecision{success: true, message: "must not leak"}, errors.New("private infrastructure detail"), http.StatusInternalServerError, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &webhookHandler{logger: testLogger(t)}
			response := httptest.NewRecorder()
			h.writeDecision(response, test.decision, test.err)
			var body map[string]interface{}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || body["success"] != (test.err == nil && test.decision.success) {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
			}
			message, present := body["message"]
			if present != (test.message != "") || (present && message != test.message) {
				t.Fatalf("unexpected message field: %s", response.Body)
			}
			if strings.Contains(response.Body.String(), "private infrastructure detail") {
				t.Fatal("infrastructure error leaked into response")
			}
		})
	}
}

// The reference v0.6.0 consumer is strict, not forward-compatible with message.
// Keep this dependency explicit: the built-image SSH test must use a patched
// gateway selected from the chart, not just decode into our extended Go type.
func TestStockClientRequiresMessageCapableGateway(t *testing.T) {
	meta := metadata.NewTestAuthenticatingMetadata("ubuntu")
	for _, test := range []struct {
		name     string
		decision authDecision
		wantErr  bool
	}{
		{"success", allow(meta.Authenticated("alice")), false},
		{"message-less denial", deny(meta.AuthFailed(), ""), false},
		{"message-bearing denial requires patch", deny(meta.AuthFailed(), "denied\n"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &webhookHandler{logger: testLogger(t)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.writeDecision(w, test.decision, nil)
			}))
			defer server.Close()
			client, err := authWebhook.NewTestClient(config.AuthWebhookClientConfig{
				HTTPClientConfiguration: config.HTTPClientConfiguration{URL: server.URL, Timeout: time.Second},
				AuthTimeout:             100 * time.Millisecond,
			}, testLogger(t))
			if err != nil {
				t.Fatal(err)
			}
			response := client.Password(meta, []byte("fixture-password"))
			if (response.Error() != nil) != test.wantErr || response.Success() != test.decision.success {
				t.Fatalf("success=%v error=%v", response.Success(), response.Error())
			}
			if test.wantErr && !strings.Contains(response.Error().Error(), `unknown field "message"`) {
				t.Fatalf("expected strict decoding rejection, got %v", response.Error())
			}
		})
	}
}
