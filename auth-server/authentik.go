package main

import (
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

const (
	// attrSSHPublicKey is the authentik user attribute searched during SSH
	// login. Its value must be a single-element JSON list containing the
	// canonical public key (type + base64, without a comment).
	attrSSHPublicKey = "sshPublicKey"

	authentikRestAPIV3 = "/api/v3/core/users/"
)

// authentikUser is the subset of authentik's User serializer used by the auth
// and authorization handlers. Extra fields support the in-memory API tests.
type authentikUser struct {
	PK         int                    `json:"pk"`
	UUID       string                 `json:"uuid"`
	Username   string                 `json:"username"`
	Name       string                 `json:"name"`
	IsActive   bool                   `json:"is_active"`
	Attributes map[string]interface{} `json:"attributes"`
	Groups     []authentikGroup       `json:"groups_obj"`
}

type authentikGroup struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

type paginatedUsers struct {
	Pagination struct {
		Count      int `json:"count"`
		Current    int `json:"current"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
	Results []authentikUser `json:"results"`
}

type authentikConfig struct {
	BaseURL    string
	ReadToken  string
	HTTPClient *http.Client
}

type authentikClient struct {
	cfg authentikConfig
}

// lookupUser performs one exact authentik query for a single-element list
// containing the canonical SSH public key. Exactly one result returns its
// owner; zero or multiple results are a clean authentication miss.
func (c *authentikClient) lookupUser(
	ctx context.Context,
	canonicalKey string,
) (*authentikUser, error) {
	filter, err := json.Marshal(map[string][]string{
		attrSSHPublicKey: {canonicalKey},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode SSH key filter: %w", err)
	}

	page, err := c.listUsers(ctx, url.Values{
		"attributes": {string(filter)},
	})
	if err != nil {
		return nil, err
	}
	if page.Pagination.Count != 1 || len(page.Results) != 1 {
		return nil, nil
	}
	return &page.Results[0], nil
}

// userInGroup reports whether the exact username belongs to the named group.
func (c *authentikClient) userInGroup(ctx context.Context, username, group string) (bool, error) {
	page, err := c.listUsers(ctx, url.Values{
		"username":       {username},
		"groups_by_name": {group},
	})
	if err != nil {
		return false, err
	}
	return page.Pagination.Count > 0, nil
}

func (c *authentikClient) listUsers(ctx context.Context, values url.Values) (*paginatedUsers, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usersURL(values), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build authentik users request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.ReadToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authentik users request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError("users request", resp)
	}

	var page paginatedUsers
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("failed to decode authentik users response: %w", err)
	}
	if page.Results == nil {
		page.Results = []authentikUser{}
	}
	return &page, nil
}

// statusError deliberately omits the request URL because the SSH public key is
// carried in its query string.
func statusError(operation string, resp *http.Response) error {
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf(
		"authentik %s failed with status %s: %s",
		operation, resp.Status, strings.TrimSpace(string(excerpt)),
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
// AUTHENTIK_INSECURE_SKIP_VERIFY, with a bounded request timeout.
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

func readFileOrLiteral(value string) ([]byte, error) {
	if data, err := os.ReadFile(value); err == nil {
		return data, nil
	}
	return []byte(value), nil
}
