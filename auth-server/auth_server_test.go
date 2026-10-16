package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.containerssh.io/containerssh/auth"
	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/metadata"
)

func newTestKey(t *testing.T) string {
	t.Helper()
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generate key data: %v", err)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(data)
}

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	logger, err := log.NewLogger(config.LogConfig{
		Level:       config.LogLevelCritical,
		Format:      config.LogFormatLJSON,
		Destination: config.LogDestinationStdout,
	})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return logger
}

func userWithKey(username, key string, active bool) authentikUser {
	return authentikUser{
		Username: username,
		IsActive: active,
		Attributes: map[string]interface{}{
			attrSSHPublicKeyDefault: []interface{}{key},
		},
	}
}

func userWithCustomAttr(username, attr string, value interface{}) authentikUser {
	return authentikUser{
		Username:   "alice",
		IsActive:   true,
		Attributes: map[string]interface{}{attr: value},
	}
}

// mockAuthentik implements only the exact users query used in production.
type mockAuthentik struct {
	t           *testing.T
	users       []authentikUser
	mu          sync.Mutex
	statusCode  int
	getRequests int
}

func (m *mockAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusCode != 0 {
		w.WriteHeader(m.statusCode)
		return
	}
	if r.Method != http.MethodGet || r.URL.Path != authentikRestAPIV3 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	m.getRequests++

	var filter map[string]interface{}
	if raw := r.URL.Query().Get("attributes"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &filter); err != nil {
			m.t.Errorf("invalid attributes filter: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	filtered := []authentikUser{}
	for _, user := range m.users {
		match := true
		for key, expected := range filter {
			storedJSON, _ := json.Marshal(user.Attributes[key])
			expectedJSON, _ := json.Marshal(expected)
			if string(storedJSON) != string(expectedJSON) {
				match = false
				break
			}
		}
		if name := r.URL.Query().Get("username"); name != "" && user.Username != name {
			match = false
		}
		if group := r.URL.Query().Get("groups_by_name"); group != "" {
			member := false
			for _, g := range user.Groups {
				if g.Name == group {
					member = true
					break
				}
			}
			if !member {
				match = false
			}
		}
		if match {
			filtered = append(filtered, user)
		}
	}

	response := paginatedUsers{Results: filtered}
	response.Pagination.Count = len(filtered)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func newMockClient(t *testing.T, mock *mockAuthentik) *authentikClient {
	t.Helper()
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	return &authentikClient{cfg: authentikConfig{
		BaseURL:    server.URL,
		ReadToken:  "read-token",
		HTTPClient: server.Client(),
	}}
}

func TestLookupUser(t *testing.T) {
	aliceKey := newTestKey(t)
	bobKey := newTestKey(t)

	t.Run("no user owns the key", func(t *testing.T) {
		user, err := newMockClient(t, &mockAuthentik{t: t}).lookupUser(context.Background(), aliceKey)
		if err != nil || user != nil {
			t.Fatalf("expected clean miss, got user=%+v err=%v", user, err)
		}
	})

	t.Run("exactly one user", func(t *testing.T) {
		mock := &mockAuthentik{t: t, users: []authentikUser{userWithKey("alice", aliceKey, true)}}
		user, err := newMockClient(t, mock).lookupUser(context.Background(), aliceKey)
		if err != nil || user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got user=%+v err=%v", user, err)
		}
	})

	t.Run("multiple users deny cleanly", func(t *testing.T) {
		mock := &mockAuthentik{t: t, users: []authentikUser{
			userWithKey("alice", aliceKey, true),
			userWithKey("mallory", aliceKey, true),
		}}
		user, err := newMockClient(t, mock).lookupUser(context.Background(), aliceKey)
		if err != nil || user != nil {
			t.Fatalf("expected duplicate key denial, got user=%+v err=%v", user, err)
		}
	})

	t.Run("api failure is surfaced", func(t *testing.T) {
		mock := &mockAuthentik{t: t, statusCode: http.StatusInternalServerError}
		if _, err := newMockClient(t, mock).lookupUser(context.Background(), bobKey); err == nil {
			t.Fatal("expected API error")
		}
	})
}

func TestLookupByKeyAttribute(t *testing.T) {
	key := newTestKey(t)
	userWithValue := func(value interface{}) authentikUser {
		return authentikUser{
			Username:   "alice",
			IsActive:   true,
			Attributes: map[string]interface{}{attrSSHPublicKeyDefault: value},
		}
	}
	lookup := func(t *testing.T, mock *mockAuthentik) (*authentikUser, error) {
		t.Helper()
		return newMockClient(t, mock).lookupUser(context.Background(), key)
	}

	t.Run("exact single-element list returns its sole user", func(t *testing.T) {
		mock := &mockAuthentik{t: t, users: []authentikUser{userWithValue([]interface{}{key})}}
		user, err := lookup(t, mock)
		if err != nil || user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got user=%+v err=%v", user, err)
		}
		if mock.getRequests != 1 {
			t.Fatalf("expected one API query, got %d", mock.getRequests)
		}
	})

	t.Run("different commented value is an exact miss", func(t *testing.T) {
		mock := &mockAuthentik{t: t, users: []authentikUser{userWithValue([]interface{}{key + " alice@laptop"})}}
		user, err := lookup(t, mock)
		if err != nil || user != nil {
			t.Fatalf("expected exact-match denial, got user=%+v err=%v", user, err)
		}
		if mock.getRequests != 1 {
			t.Fatalf("expected one API query, got %d", mock.getRequests)
		}
	})

	t.Run("scalar value is an exact miss", func(t *testing.T) {
		mock := &mockAuthentik{t: t, users: []authentikUser{userWithValue(key)}}
		user, err := lookup(t, mock)
		if err != nil || user != nil {
			t.Fatalf("expected list-shaped exact-match denial, got user=%+v err=%v", user, err)
		}
	})

	t.Run("no user denies after one request", func(t *testing.T) {
		mock := &mockAuthentik{t: t}
		user, err := lookup(t, mock)
		if err != nil || user != nil {
			t.Fatalf("expected clean denial, got user=%+v err=%v", user, err)
		}
		if mock.getRequests != 1 {
			t.Fatalf("expected one API query, got %d", mock.getRequests)
		}
	})

	t.Run("multiple users deny cleanly", func(t *testing.T) {
		value := []interface{}{key}
		mock := &mockAuthentik{t: t, users: []authentikUser{userWithValue(value), userWithValue(value)}}
		user, err := lookup(t, mock)
		if err != nil || user != nil {
			t.Fatalf("expected duplicate key denial, got user=%+v err=%v", user, err)
		}
		if mock.getRequests != 1 {
			t.Fatalf("expected one API query, got %d", mock.getRequests)
		}
	})

	t.Run("api failure does not leak the key", func(t *testing.T) {
		mock := &mockAuthentik{t: t, statusCode: http.StatusInternalServerError}
		_, err := lookup(t, mock)
		if err == nil {
			t.Fatal("expected API error")
		}
		if strings.Contains(err.Error(), key) {
			t.Fatalf("API error contains public key material: %v", err)
		}
	})

	t.Run("custom attribute name is honoured", func(t *testing.T) {
		client := newMockClient(t, &mockAuthentik{t: t, users: []authentikUser{userWithCustomAttr(
			"alice", "myCustomKey", []interface{}{key},
		)}})
		client.cfg.KeyAttribute = "myCustomKey"
		user, err := client.lookupUser(context.Background(), key)
		if err != nil || user == nil || user.Username != "alice" {
			t.Fatalf("expected alice via custom attribute, got user=%+v err=%v", user, err)
		}
	})
}

func TestOnPubKey(t *testing.T) {
	aliceKey := newTestKey(t)
	bobKey := newTestKey(t)
	newHandler := func(users ...authentikUser) *authHandler {
		return &authHandler{
			authentik: newMockClient(t, &mockAuthentik{t: t, users: users}),
			logger:    testLogger(t),
		}
	}

	t.Run("valid key succeeds as owner", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, true))
		decision, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !decision.success || decision.meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice, got success=%v owner=%q err=%v", decision.success, decision.meta.AuthenticatedUsername, err)
		}
		if decision.message != "" {
			t.Fatalf("success must not carry a denial message, got %q", decision.message)
		}
	})

	t.Run("key not enrolled is denied with a user-facing message", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, true))
		decision, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: bobKey},
		)
		if decision.success || err != nil {
			t.Fatalf("expected clean denial, got success=%v err=%v", decision.success, err)
		}
		if decision.message != "public key is not enrolled for any user\n" {
			t.Fatalf("unexpected denial message %q", decision.message)
		}
	})

	t.Run("requested username does not replace authenticated owner", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, true))
		decision, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("ubuntu"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !decision.success || decision.meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice owner, got success=%v owner=%q err=%v", decision.success, decision.meta.AuthenticatedUsername, err)
		}
	})

	t.Run("inactive owner is denied with a user-facing message", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, false))
		decision, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if decision.success || err != nil {
			t.Fatalf("expected inactive-user denial, got success=%v err=%v", decision.success, err)
		}
		if decision.message != "the account owning this key is inactive\n" {
			t.Fatalf("unexpected denial message %q", decision.message)
		}
	})

	t.Run("authentik failure denies without a user-facing message", func(t *testing.T) {
		mock := &mockAuthentik{t: t, statusCode: http.StatusInternalServerError}
		handler := &authHandler{authentik: newMockClient(t, mock), logger: testLogger(t)}
		decision, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if decision.success || err == nil {
			t.Fatalf("expected fail-closed error, got success=%v err=%v", decision.success, err)
		}
		if decision.message != "" {
			t.Fatalf("infrastructure failure must not carry a user-facing message, got %q", decision.message)
		}
	})
}

func TestOnPubKeyWithKeyAttribute(t *testing.T) {
	key := newTestKey(t)
	newHandler := func(value interface{}) *authHandler {
		user := authentikUser{
			Username:   "alice",
			IsActive:   true,
			Attributes: map[string]interface{}{attrSSHPublicKeyDefault: value},
		}
		return &authHandler{
			authentik: newMockClient(t, &mockAuthentik{t: t, users: []authentikUser{user}}),
			logger:    testLogger(t),
		}
	}

	t.Run("exact single-element list authenticates", func(t *testing.T) {
		decision, err := newHandler([]interface{}{key}).OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: key},
		)
		if err != nil || !decision.success || decision.meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice, got success=%v owner=%q err=%v", decision.success, decision.meta.AuthenticatedUsername, err)
		}
	})

	t.Run("comment is preserved for exact matching", func(t *testing.T) {
		commented := key + " alice@laptop"
		decision, err := newHandler([]interface{}{commented}).OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: commented},
		)
		if err != nil || !decision.success {
			t.Fatalf("expected commented exact match, got success=%v err=%v", decision.success, err)
		}
	})
}

func TestOnAuthorizationGroupGate(t *testing.T) {
	key := newTestKey(t)
	alice := userWithKey("alice", key, true)
	alice.Groups = []authentikGroup{{Name: "ssh-users"}}
	newHandler := func(group string) *authHandler {
		return &authHandler{
			authentik: newMockClient(t, &mockAuthentik{t: t, users: []authentikUser{alice}}),
			cfg:       authConfig{RequireGroup: group},
			logger:    testLogger(t),
		}
	}
	meta := metadata.NewTestAuthenticatingMetadata("pod-template").Authenticated("alice")

	t.Run("member of the required group is allowed", func(t *testing.T) {
		decision, err := newHandler("ssh-users").OnAuthorization(meta)
		if err != nil || !decision.success {
			t.Fatalf("expected group allow, got success=%v err=%v", decision.success, err)
		}
		if decision.message != "" {
			t.Fatalf("success must not carry a denial message, got %q", decision.message)
		}
	})

	t.Run("non-member is denied with a user-facing message", func(t *testing.T) {
		decision, err := newHandler("admins").OnAuthorization(meta)
		if decision.success || err != nil {
			t.Fatalf("expected group deny, got success=%v err=%v", decision.success, err)
		}
		if decision.message != "user 'alice' is not a member of the required group 'admins'\n" {
			t.Fatalf("unexpected denial message %q", decision.message)
		}
	})
}

// postWebhook posts a JSON request body to the webhook endpoint and returns the
// status code and the decoded response body.
func postWebhook(t *testing.T, serverURL, endpoint, body string) (int, authResponseBody) {
	t.Helper()
	response, err := http.Post(serverURL+"/"+endpoint, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /%s: %v", endpoint, err)
	}
	defer response.Body.Close()
	var decoded authResponseBody
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode /%s response: %v", endpoint, err)
	}
	return response.StatusCode, decoded
}

// TestWebhookUserFacingMessages verifies the wire contract the fork gateway
// (PR #10) relies on: policy denials are HTTP 200 and carry the user-facing
// message; infrastructure failures are HTTP 500 without one; successes are
// HTTP 200 without one.
func TestWebhookUserFacingMessages(t *testing.T) {
	enrolledKey := newTestKey(t)
	mock := &mockAuthentik{t: t, users: []authentikUser{userWithKey("alice", enrolledKey, true)}}
	server := httptest.NewServer(newWebhookHandler(&authHandler{
		authentik: newMockClient(t, mock),
		cfg:       authConfig{AllowedTemplates: []string{"ubuntu"}},
		logger:    testLogger(t),
	}, testLogger(t)))
	defer server.Close()

	t.Run("pubkey denial carries the enrollment message", func(t *testing.T) {
		status, body := postWebhook(t, server.URL, "pubkey",
			`{"username":"pod-template","publicKey":"ssh-ed25519 unknown"}`)
		if status != http.StatusOK {
			t.Fatalf("expected HTTP 200 for a policy denial, got %d", status)
		}
		if body.Success {
			t.Fatal("expected denial")
		}
		if body.Message != "public key is not enrolled for any user\n" {
			t.Fatalf("unexpected message %q", body.Message)
		}
	})

	t.Run("pubkey success has no message", func(t *testing.T) {
		status, body := postWebhook(t, server.URL, "pubkey",
			fmt.Sprintf(`{"username":"ubuntu","publicKey":%q}`, enrolledKey))
		if status != http.StatusOK || !body.Success {
			t.Fatalf("expected success, got status=%d success=%v", status, body.Success)
		}
		if body.Message != "" {
			t.Fatalf("success must not carry a message, got %q", body.Message)
		}
		if body.AuthenticatedUsername != "alice" {
			t.Fatalf("expected owner alice, got %q", body.AuthenticatedUsername)
		}
	})

	t.Run("unknown template denial carries the template message", func(t *testing.T) {
		status, body := postWebhook(t, server.URL, "authz",
			`{"username":"unknown-template","authenticatedUsername":"alice"}`)
		if status != http.StatusOK {
			t.Fatalf("expected HTTP 200 for a policy denial, got %d", status)
		}
		if body.Success {
			t.Fatal("expected denial")
		}
		if body.Message != "no pod template matches SSH username 'unknown-template'\n" {
			t.Fatalf("unexpected message %q", body.Message)
		}
	})

	t.Run("allowed template authorizes without a message", func(t *testing.T) {
		status, body := postWebhook(t, server.URL, "authz",
			`{"username":"ubuntu","authenticatedUsername":"alice"}`)
		if status != http.StatusOK || !body.Success {
			t.Fatalf("expected success, got status=%d success=%v", status, body.Success)
		}
		if body.Message != "" {
			t.Fatalf("success must not carry a message, got %q", body.Message)
		}
	})

	t.Run("password denial carries the not-supported message", func(t *testing.T) {
		status, body := postWebhook(t, server.URL, "password",
			`{"username":"ubuntu","passwordBase64":"cGFzcw=="}`)
		if status != http.StatusOK || body.Success {
			t.Fatalf("expected denial, got status=%d success=%v", status, body.Success)
		}
		if body.Message != "password authentication is not supported; use public key authentication\n" {
			t.Fatalf("unexpected message %q", body.Message)
		}
	})

	t.Run("authentik outage is HTTP 500 without a message", func(t *testing.T) {
		mock.statusCode = http.StatusInternalServerError
		defer func() { mock.statusCode = 0 }()
		status, body := postWebhook(t, server.URL, "pubkey",
			fmt.Sprintf(`{"username":"ubuntu","publicKey":%q}`, enrolledKey))
		if status != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500 for an infrastructure failure, got %d", status)
		}
		if body.Success {
			t.Fatal("expected fail-closed denial")
		}
		if body.Message != "" {
			t.Fatalf("infrastructure failure must not carry a message, got %q", body.Message)
		}
	})

	t.Run("unknown paths answer 404", func(t *testing.T) {
		response, err := http.Post(server.URL+"/config", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST /config: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 for unknown path, got %d", response.StatusCode)
		}
	})
}
