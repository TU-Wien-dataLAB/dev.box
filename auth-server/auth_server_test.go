package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
		ok, meta, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !ok || meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice, got ok=%v owner=%q err=%v", ok, meta.AuthenticatedUsername, err)
		}
	})

	t.Run("key not enrolled is denied", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, true))
		ok, _, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: bobKey},
		)
		if ok || err != nil {
			t.Fatalf("expected clean denial, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("requested username does not replace authenticated owner", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, true))
		ok, meta, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("ubuntu"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !ok || meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice owner, got ok=%v owner=%q err=%v", ok, meta.AuthenticatedUsername, err)
		}
	})

	t.Run("inactive owner is denied", func(t *testing.T) {
		handler := newHandler(userWithKey("alice", aliceKey, false))
		ok, _, err := handler.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if ok || err != nil {
			t.Fatalf("expected inactive-user denial, got ok=%v err=%v", ok, err)
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
		ok, meta, err := newHandler([]interface{}{key}).OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: key},
		)
		if err != nil || !ok || meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected alice, got ok=%v owner=%q err=%v", ok, meta.AuthenticatedUsername, err)
		}
	})

	t.Run("comment is preserved for exact matching", func(t *testing.T) {
		commented := key + " alice@laptop"
		ok, _, err := newHandler([]interface{}{commented}).OnPubKey(
			metadata.NewTestAuthenticatingMetadata("pod-template"),
			auth.PublicKey{PublicKey: commented},
		)
		if err != nil || !ok {
			t.Fatalf("expected commented exact match, got ok=%v err=%v", ok, err)
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
		ok, _, err := newHandler("ssh-users").OnAuthorization(meta)
		if err != nil || !ok {
			t.Fatalf("expected group allow, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("non-member is denied", func(t *testing.T) {
		ok, _, err := newHandler("admins").OnAuthorization(meta)
		if ok || err != nil {
			t.Fatalf("expected group deny, got ok=%v err=%v", ok, err)
		}
	})
}
