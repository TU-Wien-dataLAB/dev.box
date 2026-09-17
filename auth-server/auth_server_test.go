package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.containerssh.io/containerssh/auth"
	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/metadata"

	"golang.org/x/crypto/ssh"
)

// ---- helpers ---------------------------------------------------------------

// newTestKey returns a fresh ed25519 public key and its armored line.
func newTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("wrap public key: %v", err)
	}
	return pub, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
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

func userWithKey(u authentikUser, key string) authentikUser {
	if u.Attributes == nil {
		u.Attributes = map[string]interface{}{}
	}
	u.Attributes[attrSSHPublicKey] = []interface{}{key}
	return u
}

// ---- public-key canonicalization ------------------------------------------

func TestCanonicalizeAuthorizedKeyDropsComment(t *testing.T) {
	pub, armored := newTestKey(t)
	canonical, err := canonicalizeAuthorizedKey(armored + " bob@laptop\n")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if canonical != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) {
		t.Fatalf("canonical key includes a comment: %q", canonical)
	}
}

func TestCanonicalizeAuthorizedKeyRejectsGarbage(t *testing.T) {
	if _, err := canonicalizeAuthorizedKey("not a key at all"); err == nil {
		t.Fatal("expected parse error for garbage input")
	}
}

// ---- mock authentik --------------------------------------------------------

// mockAuthentik is a tiny in-memory re-implementation of the users API subset
// the server relies on.
type mockAuthentik struct {
	t           *testing.T
	users       []authentikUser
	mu          sync.Mutex
	statusCode  int // if non-zero, serve this status for every request
	getRequests int // number of users-list GETs; verifies exact lookup stays one request
}

func (m *mockAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusCode != 0 {
		w.WriteHeader(m.statusCode)
		return
	}
	p := r.URL.Path
	if r.Method == http.MethodGet && p == "/api/v3/core/users/" {
		m.getRequests++
		q := r.URL.Query()
		filtered := []authentikUser{}
		for _, u := range m.users {
			if ss, ok := q["attributes"]; ok && len(ss) > 0 {
				// authentik semantics: the attribute value must equal the
				// filter value as JSON (scalar vs scalar, list vs list).
				var filter map[string]interface{}
				if err := json.Unmarshal([]byte(ss[0]), &filter); err != nil {
					m.t.Errorf("bad attributes filter %q: %v", ss[0], err)
				}
				mismatch := false
				for k, v := range filter {
					storedJSON, _ := json.Marshal(u.Attributes[k])
					filterJSON, _ := json.Marshal(v)
					if string(storedJSON) != string(filterJSON) {
						mismatch = true
						break
					}
				}
				if mismatch {
					continue
				}
			}
			if name := q.Get("username"); name != "" && u.Username != name {
				continue
			}
			if group := q.Get("groups_by_name"); group != "" {
				ok := false
				for _, g := range u.Groups {
					if g.Name == group {
						ok = true
						break
					}
				}
				if !ok {
					continue
				}
			}
			if uu, ok := q["uuid"]; ok && len(uu) > 0 {
				for _, n := range uu {
					if u.UUID == n {
						filtered = append(filtered, u)
					}
				}
				continue
			}
			filtered = append(filtered, u)
		}
		// Server-side paging, mirroring the DRF/authentik behavior: filters
		// apply first, then the page is cut from the result set.
		totalMatches := len(filtered)
		pageSize := 100
		if ps := q.Get("page_size"); ps != "" {
			if n, err := strconv.Atoi(ps); err == nil && n > 0 {
				pageSize = n
			}
		}
		pageNo := 1
		if p := q.Get("page"); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				pageNo = n
			}
		}
		totalPages := (len(filtered) + pageSize - 1) / pageSize
		if totalPages == 0 {
			totalPages = 1
		}
		start := (pageNo - 1) * pageSize
		if start > len(filtered) {
			start = len(filtered)
		}
		end := start + pageSize
		if end > len(filtered) {
			end = len(filtered)
		}
		filtered = filtered[start:end]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(paginatedUsers{
			Results: filtered,
			Pagination: struct {
				Count      int `json:"count"`
				Current    int `json:"current"`
				TotalPages int `json:"total_pages"`
			}{Count: totalMatches, Current: pageNo, TotalPages: totalPages},
		})
		return
	}
	w.WriteHeader(404)
}

func newMockClient(t *testing.T, m *mockAuthentik) *authentikClient {
	t.Helper()
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return &authentikClient{cfg: authentikConfig{
		BaseURL:    srv.URL,
		ReadToken:  "read-token",
		HTTPClient: srv.Client(),
	}}
}

// ---- lookup semantics ------------------------------------------------------

func TestLookupUser(t *testing.T) {
	_, aliceKey := newTestKey(t)
	_, bobKey := newTestKey(t)

	t.Run("no user owns the key", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{}}
		c := newMockClient(t, m)
		user, err := c.lookupUser(context.Background(), aliceKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user != nil {
			t.Fatalf("expected no user, got %+v", user)
		}
	})

	t.Run("exactly one user", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey),
		}}
		c := newMockClient(t, m)
		user, err := c.lookupUser(context.Background(), aliceKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
	})

	t.Run("multiple users deny cleanly", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey),
			userWithKey(authentikUser{PK: 2, UUID: "u2", Username: "mallory", IsActive: true}, aliceKey),
		}}
		c := newMockClient(t, m)
		user, err := c.lookupUser(context.Background(), aliceKey)
		if err != nil || user != nil {
			t.Fatalf("expected duplicate key to deny cleanly, got user=%+v err=%v", user, err)
		}
	})

	t.Run("api failure is surfaced", func(t *testing.T) {
		m := &mockAuthentik{t: t, statusCode: 500}
		c := newMockClient(t, m)
		if _, err := c.lookupUser(context.Background(), bobKey); err == nil {
			t.Fatal("expected error for API failure")
		}
	})
}

// ---- exact public-key attribute semantics ---------------------------------

func TestLookupByKeyAttribute(t *testing.T) {
	_, aliceKey := newTestKey(t)
	canonical := strings.TrimSpace(aliceKey)

	userWithRawKey := func(u authentikUser, value interface{}) authentikUser {
		if u.Attributes == nil {
			u.Attributes = map[string]interface{}{}
		}
		u.Attributes[attrSSHPublicKey] = value
		return u
	}
	lookup := func(t *testing.T, m *mockAuthentik) (*authentikUser, error) {
		t.Helper()
		return newMockClient(t, m).lookupUser(context.Background(), canonical)
	}

	t.Run("exact single-element list returns its sole user", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(
				authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true},
				[]interface{}{canonical},
			),
		}}
		user, err := lookup(t, m)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
		if m.getRequests != 1 {
			t.Fatalf("expected one exact API query, got %d", m.getRequests)
		}
	})

	t.Run("commented list value is an exact miss", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(
				authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true},
				[]interface{}{canonical + " alice@laptop"},
			),
		}}
		user, err := lookup(t, m)
		if err != nil || user != nil {
			t.Fatalf("expected clean exact-match denial, got user=%+v err=%v", user, err)
		}
		if m.getRequests != 1 {
			t.Fatalf("expected no scan after the exact miss, got %d requests", m.getRequests)
		}
	})

	t.Run("scalar value is an exact miss", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(
				authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true},
				canonical,
			),
		}}
		user, err := lookup(t, m)
		if err != nil || user != nil {
			t.Fatalf("expected list-shaped exact-match denial, got user=%+v err=%v", user, err)
		}
	})

	t.Run("no user denies after one request", func(t *testing.T) {
		m := &mockAuthentik{t: t}
		user, err := lookup(t, m)
		if err != nil || user != nil {
			t.Fatalf("expected clean denial, got user=%+v err=%v", user, err)
		}
		if m.getRequests != 1 {
			t.Fatalf("expected one exact API query, got %d", m.getRequests)
		}
	})

	t.Run("multiple users deny cleanly", func(t *testing.T) {
		value := []interface{}{canonical}
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, value),
			userWithRawKey(authentikUser{PK: 2, UUID: "u2", Username: "mallory", IsActive: true}, value),
		}}
		user, err := lookup(t, m)
		if err != nil || user != nil {
			t.Fatalf("expected duplicate key to deny cleanly, got user=%+v err=%v", user, err)
		}
		if m.getRequests != 1 {
			t.Fatalf("expected one exact API query, got %d", m.getRequests)
		}
	})

	t.Run("api failure is surfaced without leaking the key", func(t *testing.T) {
		m := &mockAuthentik{t: t, statusCode: 500}
		_, err := lookup(t, m)
		if err == nil {
			t.Fatal("expected error for API failure")
		}
		if strings.Contains(err.Error(), canonical) {
			t.Fatalf("API error contains public key material: %v", err)
		}
	})
}

// ---- OnPubKey ---------------------------------------------------------------

func TestOnPubKey(t *testing.T) {
	_, aliceKey := newTestKey(t)
	_, bobKey := newTestKey(t)
	alice := userWithKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey)

	newHandler := func(enforce bool) *authHandler {
		m := &mockAuthentik{t: t, users: []authentikUser{alice}}
		return &authHandler{
			authentik: newMockClient(t, m),
			cfg:       authConfig{EnforceUsername: enforce, PasswordUsers: map[string]struct{}{}},
			logger:    testLogger(t),
		}
	}

	t.Run("valid key + matching username succeeds as owner", func(t *testing.T) {
		h := newHandler(true)
		ok, meta, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("alice"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !ok {
			t.Fatalf("expected success, got ok=%v err=%v", ok, err)
		}
		if meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected authenticated as alice, got %q", meta.AuthenticatedUsername)
		}
	})

	t.Run("key not enrolled is denied", func(t *testing.T) {
		h := newHandler(true)
		ok, _, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("bob"),
			auth.PublicKey{PublicKey: bobKey},
		)
		if ok || err != nil {
			t.Fatalf("expected clean denial, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("enforce-username denies impersonation", func(t *testing.T) {
		h := newHandler(true)
		ok, _, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("bob"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if ok || err != nil {
			t.Fatalf("expected denied impersonation, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("enforce-username off authenticates as owner", func(t *testing.T) {
		h := newHandler(false)
		ok, meta, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("anything"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !ok {
			t.Fatalf("expected success, got ok=%v err=%v", ok, err)
		}
		if meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected authenticated as alice, got %q", meta.AuthenticatedUsername)
		}
	})

	t.Run("inactive owner is denied", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithKey(authentikUser{PK: 2, UUID: "u2", Username: "gone", IsActive: false}, aliceKey),
		}}
		h := &authHandler{
			authentik: newMockClient(t, m),
			cfg:       authConfig{EnforceUsername: true, PasswordUsers: map[string]struct{}{}},
			logger:    testLogger(t),
		}
		ok, _, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("gone"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if ok || err != nil {
			t.Fatalf("expected denied for inactive user, got ok=%v err=%v", ok, err)
		}
	})
}

// ---- OnPubKey exact attribute semantics ------------------------------------

func TestOnPubKeyWithKeyAttribute(t *testing.T) {
	_, aliceKey := newTestKey(t)
	canonical := strings.TrimSpace(aliceKey)

	newHandler := func(value interface{}) *authHandler {
		m := &mockAuthentik{t: t, users: []authentikUser{func() authentikUser {
			u := authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}
			u.Attributes = map[string]interface{}{attrSSHPublicKey: value}
			return u
		}()}}
		return &authHandler{
			authentik: newMockClient(t, m),
			cfg:       authConfig{EnforceUsername: true, PasswordUsers: map[string]struct{}{}},
			logger:    testLogger(t),
		}
	}

	t.Run("canonical single-element list authenticates", func(t *testing.T) {
		h := newHandler([]interface{}{canonical})
		ok, meta, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("alice"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || !ok {
			t.Fatalf("expected success via attributes.sshPublicKey, got ok=%v err=%v", ok, err)
		}
		if meta.AuthenticatedUsername != "alice" {
			t.Fatalf("expected authenticated as alice, got %q", meta.AuthenticatedUsername)
		}
	})

	t.Run("commented value is denied", func(t *testing.T) {
		h := newHandler([]interface{}{canonical + " alice@laptop"})
		ok, _, err := h.OnPubKey(
			metadata.NewTestAuthenticatingMetadata("alice"),
			auth.PublicKey{PublicKey: aliceKey},
		)
		if err != nil || ok {
			t.Fatalf("expected clean exact-match denial, got ok=%v err=%v", ok, err)
		}
	})
}

// ---- OnAuthorization ---------------------------------------------------------

func TestOnAuthorizationGroupGate(t *testing.T) {
	_, key := newTestKey(t)
	alice := userWithKey(authentikUser{
		PK: 1, UUID: "u1", Username: "alice", IsActive: true,
		Groups: []authentikGroup{{PK: "g1", Name: "ssh-users"}},
	}, key)
	m := &mockAuthentik{t: t, users: []authentikUser{alice}}
	h := &authHandler{
		authentik: newMockClient(t, m),
		cfg:       authConfig{RequireGroup: "ssh-users"},
		logger:    testLogger(t),
	}

	meta := metadata.NewTestAuthenticatingMetadata("alice").Authenticated("alice")
	if ok, _, err := h.OnAuthorization(meta); err != nil || !ok {
		t.Fatalf("expected group allow, got ok=%v err=%v", ok, err)
	}

	meta = metadata.NewTestAuthenticatingMetadata("alice").Authenticated("alice")
	h.cfg.RequireGroup = "admins"
	if ok, _, err := h.OnAuthorization(meta); ok || err != nil {
		t.Fatalf("expected group deny, got ok=%v err=%v", ok, err)
	}
}
