package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	authWebhook "go.containerssh.io/containerssh/auth/webhook"
	"go.containerssh.io/containerssh/metadata"
)

func TestUnknownTemplateAuthorizationIsCleanDenial(t *testing.T) {
	logger := testLogger(t)
	h := &authHandler{cfg: authConfig{AllowedTemplates: []string{"ubuntu", "default"}}, logger: logger}
	handler := authWebhook.NewHandler(h, logger)
	req := httptest.NewRequest(http.MethodPost, "/authz", strings.NewReader(`{"username":"unknown","authenticatedUsername":"alice","connectionId":"template-test","remoteAddress":"127.0.0.1:43210"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("authorization must return HTTP 200 to avoid retries: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Success == nil || *body.Success {
		t.Fatal("expected explicit success:false for unknown template (default must not be a catch-all)")
	}
}

func TestTemplateAuthorizationPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		allowed  []string
		username string
		want     bool
	}{
		{"disabled without bundled config server", nil, "unknown", true},
		{"empty list denies all", []string{}, "ubuntu", false},
		{"named template allows different owner", []string{"ubuntu"}, "ubuntu", true},
		{"default is an ordinary name", []string{"default"}, "default", true},
		{"empty username denied", []string{"ubuntu"}, "", false},
		{"no normalization aliases", []string{"ubuntu"}, " ubuntu", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &authHandler{cfg: authConfig{AllowedTemplates: test.allowed}, logger: testLogger(t)}
			meta := metadata.NewTestAuthenticatingMetadata(test.username).Authenticated("alice")
			ok, returned, err := h.OnAuthorization(meta)
			if err != nil || ok != test.want || returned.AuthenticatedUsername != "alice" {
				t.Fatalf("authorization ok=%v owner=%q err=%v; want ok=%v and owner alice", ok, returned.AuthenticatedUsername, err, test.want)
			}
		})
	}
}

func TestTemplateAuthorizationPreservesGroupGate(t *testing.T) {
	alice := userWithKey("alice", newTestKey(t), true)
	alice.Groups = []authentikGroup{{Name: "ssh-users"}}
	for _, group := range []string{"ssh-users", "admins"} {
		t.Run(group, func(t *testing.T) {
			h := &authHandler{
				authentik: newMockClient(t, &mockAuthentik{t: t, users: []authentikUser{alice}}),
				cfg:       authConfig{AllowedTemplates: []string{"ubuntu"}, RequireGroup: group},
				logger:    testLogger(t),
			}
			ok, _, err := h.OnAuthorization(metadata.NewTestAuthenticatingMetadata("ubuntu").Authenticated("alice"))
			if err != nil || ok != (group == "ssh-users") {
				t.Fatalf("template must not bypass group policy: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestAllowedTemplatesEnvironment(t *testing.T) {
	t.Run("unset disables policy", func(t *testing.T) {
		t.Setenv(envAllowedTemplates, "[]")
		if err := os.Unsetenv(envAllowedTemplates); err != nil {
			t.Fatal(err)
		}
		allowed, err := allowedTemplatesFromEnv()
		if allowed != nil || err != nil {
			t.Fatalf("allowed=%v err=%v, want disabled", allowed, err)
		}
	})
	for _, test := range []struct {
		raw     string
		want    []string
		invalid bool
	}{
		{`["ubuntu","dev"]`, []string{"ubuntu", "dev"}, false},
		{`[]`, []string{}, false},
		{`null`, nil, true},
		{``, nil, true},
		{`["" ]`, nil, true},
		{`"ubuntu"`, nil, true},
		{`[123]`, nil, true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv(envAllowedTemplates, test.raw)
			allowed, err := allowedTemplatesFromEnv()
			if (err != nil) != test.invalid || (!test.invalid && !reflect.DeepEqual(allowed, test.want)) {
				t.Fatalf("allowed=%v err=%v, want %v invalid=%v", allowed, err, test.want, test.invalid)
			}
		})
	}
}
