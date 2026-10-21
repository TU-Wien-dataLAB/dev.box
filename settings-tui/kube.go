package main

// The TUI's entire Kubernetes surface: get and delete one pod by its exact
// deterministic name. No list, watch, create, or any other verb, and no name
// other than the ones derived from the trusted template list. The interface
// is the offline testability seam; the fake used by the Go tests implements
// it directly and the Docker smoke drives it through a stand-in HTTP API.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// requestTimeout bounds every single API call; a slow API degrades the UI to
// Unreachable instead of hanging it.
const requestTimeout = 5 * time.Second

const (
	tokenFile     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caFile        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

var (
	// errNotFound classifies HTTP 404: the box does not exist (not created).
	errNotFound = errors.New("pod not found")
	// errForbidden classifies HTTP 403: authorization for this name changed;
	// the template list and grants are fixed per settings connection.
	errForbidden = errors.New("not authorized for this pod")
)

// boxClient is the whole Kubernetes access of the management UI.
type boxClient interface {
	GetPod(ctx context.Context, name string) (*podInfo, error)
	DeletePod(ctx context.Context, name string) error
}

// podInfo carries exactly the fields a plain pod get renders: status, start
// time, image, runtime class, restart count, and resource limits.
type podInfo struct {
	Metadata struct {
		Name              string     `json:"name"`
		DeletionTimestamp *time.Time `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		RuntimeClassName *string `json:"runtimeClassName"`
		Containers       []struct {
			Name      string `json:"name"`
			Image     string `json:"image"`
			Resources struct {
				Limits map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string     `json:"phase"`
		StartTime         *time.Time `json:"startTime"`
		ContainerStatuses []struct {
			RestartCount int32 `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// apiStatus is the Kubernetes Status object returned for errors and deletes.
type apiStatus struct {
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

// httpBoxClient talks to the pod REST endpoints over the mounted projected
// token. The token file is re-read per request so a rotated projected token
// keeps working within one settings connection.
type httpBoxClient struct {
	base      string
	namespace string
	token     string
	tokenFile string
	client    *http.Client
}

// newBoxClient builds the client from the in-cluster configuration (projected
// token, cluster CA, service env) in the settings pod's own namespace.
//
// DEV_BOX_KUBE_API_URL (with optional DEV_BOX_KUBE_API_TOKEN and
// DEV_BOX_NAMESPACE) is an offline-validation override that points the same
// code at a stand-in API server for the Docker image smoke; it is trusted
// plumbing exactly like the owner env and is never set by the config server.
func newBoxClient() (boxClient, error) {
	if override := strings.TrimSpace(os.Getenv("DEV_BOX_KUBE_API_URL")); override != "" {
		namespace, err := namespaceFrom(func() (string, error) {
			return "", errNoOverrideNamespace
		})
		if err != nil {
			return nil, err
		}
		return &httpBoxClient{
			base:      strings.TrimSuffix(override, "/"),
			namespace: namespace,
			token:     os.Getenv("DEV_BOX_KUBE_API_TOKEN"),
			client:    &http.Client{Timeout: requestTimeout},
		}, nil
	}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("no in-cluster Kubernetes environment (KUBERNETES_SERVICE_HOST/PORT unset)")
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("cluster CA is not valid PEM")
	}
	namespace, err := namespaceFrom(func() (string, error) {
		data, err := os.ReadFile(namespaceFile)
		if err != nil {
			return "", fmt.Errorf("read pod namespace: %w", err)
		}
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns, nil
		}
		return "", errors.New("pod namespace file is empty")
	})
	if err != nil {
		return nil, err
	}
	return &httpBoxClient{
		base:      "https://" + net.JoinHostPort(host, port),
		namespace: namespace,
		tokenFile: tokenFile,
		client: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		},
	}, nil
}

var errNoOverrideNamespace = errors.New("DEV_BOX_NAMESPACE is required with DEV_BOX_KUBE_API_URL")

// namespaceFrom resolves the target namespace: the trusted override first,
// then the mounted namespace file.
func namespaceFrom(file func() (string, error)) (string, error) {
	if ns := strings.TrimSpace(os.Getenv("DEV_BOX_NAMESPACE")); ns != "" {
		return ns, nil
	}
	return file()
}

// unavailableClient is used when no client could be built: every call fails
// with the construction error, so the UI renders Unreachable instead of
// crashing or silently pretending everything is fine.
type unavailableClient struct{ err error }

func (c unavailableClient) GetPod(context.Context, string) (*podInfo, error) { return nil, c.err }
func (c unavailableClient) DeletePod(context.Context, string) error          { return c.err }

func (c *httpBoxClient) request(ctx context.Context, method, name string) (*http.Response, error) {
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s", c.base, c.namespace, name)
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	token := c.token
	if c.tokenFile != "" {
		data, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read projected token: %w", err)
		}
		token = strings.TrimSpace(string(data))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.client.Do(req)
}

func (c *httpBoxClient) GetPod(ctx context.Context, name string) (*podInfo, error) {
	resp, err := c.request(ctx, http.MethodGet, name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var pod podInfo
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&pod); err != nil {
			return nil, fmt.Errorf("decode pod %s: %w", name, err)
		}
		return &pod, nil
	}
	return nil, statusError(resp)
}

func (c *httpBoxClient) DeletePod(ctx context.Context, name string) error {
	resp, err := c.request(ctx, http.MethodDelete, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		var status apiStatus
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err == nil {
			if status.Kind == "Status" && status.Status == "Failure" {
				return classify(resp.StatusCode, status.Message)
			}
		}
		return nil
	}
	return statusError(resp)
}

// statusError maps an API error response onto the sentinel classifications
// while keeping the server's message for display.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var status apiStatus
	_ = json.Unmarshal(body, &status)
	return classify(resp.StatusCode, status.Message)
}

func classify(code int, message string) error {
	var kind error
	switch code {
	case http.StatusNotFound:
		kind = errNotFound
	case http.StatusForbidden:
		kind = errForbidden
	case http.StatusUnauthorized:
		kind = fmt.Errorf("the mounted token was rejected")
	default:
		return fmt.Errorf("api error HTTP %d: %s", code, message)
	}
	if message == "" {
		return kind
	}
	return fmt.Errorf("%w: %s", kind, message)
}
