package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Attribute names used to store SSH key material on an authentik user's
// `attributes` JSON object (see auth-server/spec.md §1.4):
//
//	attributes.ssh_public_key      the armored key as entered by the user
//	attributes.ssh_key_fingerprint canonical "SHA256:..." fingerprint (derived cache)
//
// The auth server only ever *reads* these; only the optional normalizing sync
// (§5.3, option (a)) writes them.
const (
	attrSSHPublicKey      = "ssh_public_key"
	attrSSHKeyFingerprint = "ssh_key_fingerprint"

	// authentikRestAPIV3 is the API version prefix for the users API.
	authentikRestAPIV3 = "/api/v3/core/users/"
)

// authentikUser is the subset of the authentik `User` serializer we consume.
// Field names/types mirror the OpenAPI schema (see /tmp/authentik-schema.yml).
type authentikUser struct {
	// PK is the integer user id — the `{id}` path parameter for PATCH/POST.
	PK int `json:"pk"`
	// UUID is the stable string user id.
	UUID string `json:"uuid"`
	// Username is the login name ("alice").
	Username string `json:"username"`
	// Name is the display name.
	Name string `json:"name"`
	// IsActive is false for deactivated accounts.
	IsActive bool `json:"is_active"`
	// Attributes is the free-form JSON object attached to the user.
	Attributes map[string]interface{} `json:"attributes"`
	// Groups contains the group objects this user is a member of
	// (populated because the users API defaults include_groups=true).
	Groups []authentikGroup `json:"groups_obj"`
}

// authentikGroup is the subset of authentik's `PartialGroup` we need.
type authentikGroup struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

// paginatedUsers is the envelope of `GET /api/v3/core/users/`.
type paginatedUsers struct {
	Pagination struct {
		Count      int `json:"count"`
		Current    int `json:"current"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
	Results []authentikUser `json:"results"`
}

// authentikConfig is the resolved authentik connection settings.
type authentikConfig struct {
	// BaseURL is a scheme://host[:port] root, e.g. "https://authentik.example.com".
	BaseURL string
	// ReadToken is the API token used for lookups (needs: view users [+ groups]).
	ReadToken string
	// WriteToken is the API token used by the sync to PATCH users ("edit users").
	// Empty falls back to ReadToken — which then also needs edit permission.
	WriteToken string
	// HTTPClient is the configured HTTP(S) client (custom CA / insecure allowed).
	HTTPClient *http.Client
}

// authentikClient talks to the authentik `/api/v3/core/` REST API.
// It is deliberately read-only for the hot path (OnPubKey/OnAuthorization);
// only the normalizing sync uses the write token.
type authentikClient struct {
	cfg authentikConfig
}

func (c *authentikClient) tokenForWrite(write bool) string {
	if write && c.cfg.WriteToken != "" {
		return c.cfg.WriteToken
	}
	return c.cfg.ReadToken
}

// lookupByFingerprint finds the (single) user whose
// `attributes.ssh_key_fingerprint` exactly equals the given SHA256 fingerprint.
// Returns (nil, nil) when no user owns the key; an error on API trouble or when
// the fingerprint is bound to more than one user (integrity violation).
func (c *authentikClient) lookupByFingerprint(
	ctx context.Context,
	fingerprint string,
) (*authentikUser, error) {
	filter, err := json.Marshal(map[string]string{attrSSHKeyFingerprint: fingerprint})
	if err != nil {
		return nil, fmt.Errorf("failed to encode attribute filter: %w", err)
	}
	u := c.usersURL(url.Values{
		"attributes": {string(filter)},
	})
	page, err := c.listUsers(ctx, u)
	if err != nil {
		return nil, err
	}
	switch page.Pagination.Count {
	case 0:
		return nil, nil
	case 1:
		user := page.Results[0]
		return &user, nil
	default:
		return nil, fmt.Errorf(
			"fingerprint %s is bound to %d users (%s, ...): key uniqueness violated",
			fingerprint, page.Pagination.Count, page.Results[0].Username,
		)
	}
}

// userInGroup reports whether the user with exactly this username is a member
// of the given group name. Uses the users API `groups_by_name` filter.
func (c *authentikClient) userInGroup(ctx context.Context, username, group string) (bool, error) {
	u := c.usersURL(url.Values{
		"username":       {username},
		"groups_by_name": {group},
	})
	page, err := c.listUsers(ctx, u)
	if err != nil {
		return false, err
	}
	return page.Pagination.Count > 0, nil
}

// listAllUsers returns every authentik user (all pages), in username order.
func (c *authentikClient) listAllUsers(ctx context.Context) ([]authentikUser, error) {
	var all []authentikUser
	u := c.usersURL(url.Values{
		"page_size": {"100"},
		"ordering":  {"username"},
	})
	for u != "" {
		page, err := c.listUsers(ctx, u)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Pagination.Current >= page.Pagination.TotalPages || page.Pagination.TotalPages == 0 {
			break
		}
		u = c.usersURL(url.Values{
			"page":      {fmt.Sprintf("%d", page.Pagination.Current+1)},
			"page_size": {"100"},
			"ordering":  {"username"},
		})
	}
	return all, nil
}

// listUsers runs one `GET /api/v3/core/users/` call against the given URL.
func (c *authentikClient) listUsers(ctx context.Context, u string) (*paginatedUsers, error) {
	resp, err := c.do(ctx, http.MethodGet, u, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.statusError("GET "+u, resp)
	}
	var page paginatedUsers
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("failed to decode user list: %w", err)
	}
	if page.Results == nil {
		page.Results = []authentikUser{}
	}
	return &page, nil
}

// updateUserAttributes replaces the user's `attributes` object via
// `PATCH /api/v3/core/users/{id}/` (id = integer pk). Requires a write token.
func (c *authentikClient) updateUserAttributes(
	ctx context.Context,
	pk int,
	attributes map[string]interface{},
) error {
	body, err := json.Marshal(map[string]interface{}{"attributes": attributes})
	if err != nil {
		return fmt.Errorf("failed to encode attributes: %w", err)
	}
	path := fmt.Sprintf("%s/api/v3/core/users/%d/", strings.TrimRight(c.cfg.BaseURL, "/"), pk)
	resp, err := c.do(ctx, http.MethodPatch, path, body, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError("PATCH user "+fmt.Sprint(pk), resp)
	}
	return nil
}

// do builds and executes an authenticated request against the authentik API.
func (c *authentikClient) do(
	ctx context.Context,
	method, rawURL string,
	body []byte,
	write bool,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request to authentik: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForWrite(write))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authentik request failed: %w", err)
	}
	return resp, nil
}

// statusError turns a non-2xx authentik response into an error carrying the
// status code and a short body excerpt (authentik replies with DRF errors).
func (c *authentikClient) statusError(what string, resp *http.Response) error {
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf(
		"authentik %s failed with status %s: %s",
		what, resp.Status, strings.TrimSpace(string(excerpt)),
	)
}

func (c *authentikClient) usersURL(values url.Values) string {
	base, err := url.Parse(strings.TrimRight(c.cfg.BaseURL, "/"))
	if err != nil {
		base = &url.URL{}
	}
	if base.Scheme == "" {
		base.Scheme = "https"
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + authentikRestAPIV3
	u.RawQuery = values.Encode()
	return u.String()
}

// newHTTPClient builds the API HTTP client honouring AUTHENTIK_CA_FILE /
// AUTHENTIK_INSECURE_SKIP_VERIFY, with a sane default timeout.
func newHTTPClient(caFile string, insecure bool) (*http.Client, error) {
	tlsConfig := &tls.Config{
		// #nosec G402 -- explicit opt-in for self-signed dev installations.
		InsecureSkipVerify: insecure,
		MinVersion:         tls.VersionTLS12,
	}
	if caFile != "" {
		pem, err := readFileOrLiteral(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read AUTHENTIK_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("AUTHENTIK_CA_FILE contains no usable CA certificate")
		}
		tlsConfig.RootCAs = pool
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}, nil
}

// readFileOrLiteral treats the value as a file path if the file exists,
// otherwise as the literal PEM content. Lets env vars carry either a mounted
// secret file or inline PEM.
func readFileOrLiteral(value string) ([]byte, error) {
	if data, err := os.ReadFile(value); err == nil {
		return data, nil
	}
	return []byte(value), nil
}
