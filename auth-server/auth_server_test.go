package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
	u.Attributes[attrSSHPublicKey] = key
	u.Attributes[attrSSHKeyFingerprint] = fingerprintOf(key)
	return u
}

func fingerprintOf(authorized string) string {
	fp, err := fingerprintFromAuthorizedKey(authorized)
	if err != nil {
		panic(err)
	}
	return fp
}

// ---- fingerprint helpers ---------------------------------------------------

func TestFingerprintFromAuthorizedKeyIgnoresComment(t *testing.T) {
	pub, armored := newTestKey(t)

	fp, err := fingerprintFromAuthorizedKey(armored + " my-comment@host\n")
	if err != nil {
		t.Fatalf("parse with comment: %v", err)
	}
	if fp != ssh.FingerprintSHA256(pub) {
		t.Fatalf("fingerprint mismatch: got %s want %s", fp, ssh.FingerprintSHA256(pub))
	}
	fp, err = fingerprintFromAuthorizedKey(armored) // no comment
	if err != nil {
		t.Fatalf("parse without comment: %v", err)
	}
	if fp != ssh.FingerprintSHA256(pub) {
		t.Fatalf("fingerprint mismatch: got %s want %s", fp, ssh.FingerprintSHA256(pub))
	}
}

func TestFingerprintFromAuthorizedKeyRejectsGarbage(t *testing.T) {
	if _, err := fingerprintFromAuthorizedKey("not a key at all"); err == nil {
		t.Fatal("expected parse error for garbage input")
	}
}

func TestFingerprintAndCanonicalKeyDropsComment(t *testing.T) {
	pub, armored := newTestKey(t)
	_, canonical, err := fingerprintAndCanonicalKey(armored + " bob@laptop\n")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if canonical != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) {
		t.Fatalf("canonical key includes a comment: %q", canonical)
	}
}

// ---- mock authentik --------------------------------------------------------

// mockAuthentik is a tiny in-memory re-implementation of the users API subset
// the server relies on.
type mockAuthentik struct {
	t       *testing.T
	users   []authentikUser
	mu      sync.Mutex
	patches []struct {
		pk   int
		attr map[string]interface{}
	}
	statusCode int // if non-zero, serve this status for every request
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
		q := r.URL.Query()
		filtered := []authentikUser{}
		for _, u := range m.users {
			if ss, ok := q["attributes"]; ok && len(ss) > 0 {
				var filter map[string]string
				if err := json.Unmarshal([]byte(ss[0]), &filter); err != nil {
					m.t.Errorf("bad attributes filter %q: %v", ss[0], err)
				}
				// authentik semantics: exact scalar match on every listed
				// key/value pair of the user's attributes object.
				mismatch := false
				for k, v := range filter {
					stored, _ := u.Attributes[k].(string)
					if stored != v {
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
	if r.Method == http.MethodPatch && strings.HasPrefix(p, "/api/v3/core/users/") {
		var pk int
		if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(p, "/api/v3/core/users/"), "/"), "%d", &pk); err != nil {
			m.t.Errorf("bad patch path %q", p)
		}
		var body struct {
			Attributes map[string]interface{} `json:"attributes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.patches = append(m.patches, struct {
			pk   int
			attr map[string]interface{}
		}{pk, body.Attributes})
		// Apply the patch to the in-memory store so later passes see it.
		for i := range m.users {
			if m.users[i].PK == pk {
				if m.users[i].Attributes == nil {
					m.users[i].Attributes = map[string]interface{}{}
				}
				for k, v := range body.Attributes {
					m.users[i].Attributes[k] = v
				}
			}
		}
		w.WriteHeader(204)
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
		WriteToken: "write-token",
		HTTPClient: srv.Client(),
	}}
}

// ---- lookup semantics ------------------------------------------------------

func TestLookupByFingerprint(t *testing.T) {
	_, aliceKey := newTestKey(t)
	_, bobKey := newTestKey(t)

	t.Run("no user owns the key", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{}}
		c := newMockClient(t, m)
		user, err := c.lookupUser(context.Background(), fingerprintOf(aliceKey), "")
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
		user, err := c.lookupUser(context.Background(), fingerprintOf(aliceKey), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
	})

	t.Run("duplicate fingerprint is an integrity error", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey),
			userWithKey(authentikUser{PK: 2, UUID: "u2", Username: "mallory", IsActive: true}, aliceKey),
		}}
		c := newMockClient(t, m)
		if _, err := c.lookupUser(context.Background(), fingerprintOf(aliceKey), ""); err == nil {
			t.Fatal("expected integrity error for duplicate fingerprint")
		}
	})

	t.Run("api failure is surfaced", func(t *testing.T) {
		m := &mockAuthentik{t: t, statusCode: 500}
		c := newMockClient(t, m)
		if _, err := c.lookupUser(context.Background(), fingerprintOf(bobKey), ""); err == nil {
			t.Fatal("expected error for API failure")
		}
	})
}

// ---- key-attribute lookup mode (AUTH_SERVER_KEY_ATTRIBUTE) ------------------

func TestLookupByKeyAttribute(t *testing.T) {
	_, aliceKey := newTestKey(t)
	_, bobKey := newTestKey(t)
	fp := fingerprintOf(aliceKey)
	canonical := strings.TrimSpace(aliceKey)

	userWithRawKey := func(u authentikUser, key string) authentikUser {
		if u.Attributes == nil {
			u.Attributes = map[string]interface{}{}
		}
		u.Attributes["sshPublicKey"] = key
		return u
	}
	clientWithKeyAttr := func(t *testing.T, m *mockAuthentik) *authentikClient {
		c := newMockClient(t, m)
		c.cfg.KeyAttribute = "sshPublicKey"
		return c
	}

	t.Run("exact-match fast path on the canonical key", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, canonical),
		}}
		c := clientWithKeyAttr(t, m)
		user, err := c.lookupUser(context.Background(), fp, canonical)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
	})

	t.Run("scan fallback matches freeform key line with comment", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true},
				aliceKey+" alice@laptop\n"),
		}}
		c := clientWithKeyAttr(t, m)
		user, err := c.lookupUser(context.Background(), fp, canonical)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
	})

	t.Run("scan fallback handles multi-key list values", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, bobKey),
		}}
		m.mu.Lock()
		m.users[0].Attributes["sshPublicKey"] = []interface{}{
			bobKey + " old-key\n",
			aliceKey + " current-key\n",
		}
		m.mu.Unlock()
		c := clientWithKeyAttr(t, m)
		user, err := c.lookupUser(context.Background(), fp, canonical)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user == nil || user.Username != "alice" {
			t.Fatalf("expected alice, got %+v", user)
		}
	})

	t.Run("no user owns the key", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{}}
		c := clientWithKeyAttr(t, m)
		user, err := c.lookupUser(context.Background(), fp, canonical)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user != nil {
			t.Fatalf("expected no user, got %+v", user)
		}
	})

	t.Run("duplicate key across users is an integrity error", func(t *testing.T) {
		m := &mockAuthentik{t: t, users: []authentikUser{
			userWithRawKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey),
			userWithRawKey(authentikUser{PK: 2, UUID: "u2", Username: "mallory", IsActive: true}, aliceKey),
		}}
		c := clientWithKeyAttr(t, m)
		if _, err := c.lookupUser(context.Background(), fp, canonical); err == nil {
			t.Fatal("expected integrity error for duplicate key")
		}
	})

	t.Run("api failure is surfaced", func(t *testing.T) {
		m := &mockAuthentik{t: t, statusCode: 500}
		c := clientWithKeyAttr(t, m)
		if _, err := c.lookupUser(context.Background(), fp, canonical); err == nil {
			t.Fatal("expected error for API failure")
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

// ---- OnPubKey with the key-attribute lookup mode ----------------------------

func TestOnPubKeyWithKeyAttribute(t *testing.T) {
	_, aliceKey := newTestKey(t)

	// Alice stores her key freeform (with comment) under attributes.sshPublicKey.
	m := &mockAuthentik{t: t, users: []authentikUser{func() authentikUser {
		u := authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}
		u.Attributes = map[string]interface{}{"sshPublicKey": aliceKey + " alice@laptop\n"}
		return u
	}()}}
	c := newMockClient(t, m)
	c.cfg.KeyAttribute = "sshPublicKey"
	h := &authHandler{
		authentik: c,
		cfg:       authConfig{EnforceUsername: true, PasswordUsers: map[string]struct{}{}},
		logger:    testLogger(t),
	}

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

// ---- sync --------------------------------------------------------------------

func TestSyncNormalizesAndDetectsDuplicates(t *testing.T) {
	_, aliceKey := newTestKey(t)
	_, bobKey := newTestKey(t)

	alice := userWithKey(authentikUser{PK: 1, UUID: "u1", Username: "alice", IsActive: true}, aliceKey)
	alice.Attributes[attrSSHPublicKey] = aliceKey + " laptop@work\n" // dirty: comment
	bob := userWithKey(authentikUser{PK: 2, UUID: "u2", Username: "bob", IsActive: true}, bobKey)
	unrelated := authentikUser{PK: 3, UUID: "u3", Username: "user3", IsActive: true} // no key

	m := &mockAuthentik{t: t, users: []authentikUser{alice, bob, unrelated}}
	c := newMockClient(t, m)
	runner := &syncRunner{
		authentik:    c,
		writeEnabled: true,
		logger:       testLogger(t),
	}

	updated, total, problems, err := runner.syncUsers(context.Background())
	if err != nil {
		t.Fatalf("syncUsers: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected to scan 3 users, got %d", total)
	}
	if updated != 1 {
		t.Fatalf("expected exactly 1 update (alice), got %d", updated)
	}
	if len(problems) != 0 {
		t.Fatalf("expected no problems, got %v", problems)
	}
	m.mu.Lock()
	if len(m.patches) != 1 {
		m.mu.Unlock()
		t.Fatalf("expected 1 PATCH, got %d", len(m.patches))
	}
	patch := m.patches[0]
	m.mu.Unlock()
	if patch.pk != 1 {
		t.Fatalf("expected PATCH on user 1, got %d", patch.pk)
	}
	if got, _ := patch.attr[attrSSHPublicKey].(string); got != strings.TrimSpace(aliceKey)+"" {
		t.Fatalf("expected canonical public key, got %q", got)
	}
	if got, _ := patch.attr[attrSSHKeyFingerprint].(string); !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("expected SHA256 fingerprint, got %q", got)
	}

	// Second pass: nothing to do (the mock applied the first patch).
	updated, _, problems, err = runner.syncUsers(context.Background())
	if err != nil || updated != 0 || len(problems) != 0 {
		t.Fatalf("second pass should be a no-op, got updated=%d problems=%v err=%v", updated, problems, err)
	}

	// Duplicate fingerprints across users must be flagged and not written.
	dup := userWithKey(authentikUser{PK: 4, UUID: "u4", Username: "mallory", IsActive: true}, aliceKey)
	m.mu.Lock()
	m.users = append(m.users, dup)
	before := len(m.patches)
	m.mu.Unlock()
	updated, _, problems, err = runner.syncUsers(context.Background())
	if err != nil {
		t.Fatalf("syncUsers with duplicate: %v", err)
	}
	m.mu.Lock()
	writes := len(m.patches) - before
	m.mu.Unlock()
	if len(problems) == 0 {
		t.Fatal("expected a duplicate-fingerprint problem to be reported")
	}
	if writes != 0 {
		t.Fatalf("expected no writes for duplicate fingerprints, wrote %d", writes)
	}
}

// ---- concurrent paging over a large directory -------------------------------

func TestListAllUsersConcurrentPaging(t *testing.T) {
	_, key := newTestKey(t)
	fp := fingerprintOf(key)

	m := &mockAuthentik{t: t}
	for i := 1; i <= 370; i++ {
		m.users = append(m.users, authentikUser{
			PK: i, UUID: fmt.Sprintf("u%d", i),
			Username: fmt.Sprintf("user%03d", i), IsActive: true,
		})
	}
	// Target on the last page, key stored freeform (with a comment) so the
	// exact-match fast path misses and the full-scan fallback must run.
	target := userWithKey(
		authentikUser{PK: 9999, UUID: "u9999", Username: "zuser", IsActive: true},
		strings.TrimSpace(key),
	)
	target.Attributes[attrSSHPublicKey] = strings.TrimSpace(key) + " zuser@laptop\n"
	m.users = append(m.users, target)

	c := newMockClient(t, m)
	// Force the key-material scan; the stored attribute name is ssh_public_key.
	c.cfg.KeyAttribute = attrSSHPublicKey

	all, err := c.listAllUsers(context.Background())
	if err != nil {
		t.Fatalf("listAllUsers: %v", err)
	}
	if len(all) != 371 {
		t.Fatalf("expected all 371 users across pages, got %d", len(all))
	}

	user, err := c.lookupUser(context.Background(), fp, strings.TrimSpace(key))
	if err != nil {
		t.Fatalf("lookupUser: %v", err)
	}
	if user == nil || user.Username != "zuser" {
		t.Fatalf("expected zuser from the scan, got %+v", user)
	}
}
