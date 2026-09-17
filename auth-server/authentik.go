package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// attrSSHPublicKey is the authentik user attribute searched during SSH
	// login. Its value must be a single-element JSON list containing exactly
	// the public-key string supplied by ContainerSSH.
	attrSSHPublicKey = "sshPublicKey"

	authentikRestAPIV3 = "/api/v3/core/users/"
)

// authentikUser is the subset of authentik's User serializer needed to decide
// public-key authentication. Attributes and group membership support the
// handler and the in-memory API test.
type authentikUser struct {
	Username   string                 `json:"username"`
	IsActive   bool                   `json:"is_active"`
	Attributes map[string]interface{} `json:"attributes"`
	Groups     []authentikGroup       `json:"groups_obj"`
}

type authentikGroup struct {
	Name string `json:"name"`
}

type paginatedUsers struct {
	Pagination struct {
		Count int `json:"count"`
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
// containing the SSH public-key string supplied by ContainerSSH. Exactly one
// result returns its owner; zero or multiple results are a clean auth miss.
func (c *authentikClient) lookupUser(
	ctx context.Context,
	publicKey string,
) (*authentikUser, error) {
	filter, err := json.Marshal(map[string][]string{
		attrSSHPublicKey: {publicKey},
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

// newHTTPClient uses the system CA trust store and a bounded request timeout.
// TLS verification can only be disabled through the explicit development flag.
func newHTTPClient(insecure bool) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// #nosec G402 -- explicit opt-in for development installations.
				InsecureSkipVerify: insecure,
				MinVersion:         tls.VersionTLS12,
			},
		},
	}
}
