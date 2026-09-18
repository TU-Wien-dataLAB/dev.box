package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/message"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// ownerLabelKey labels every persistent user pod with its canonical owner.
	// The value is ownerLabelValue(authenticatedUsername) — a deterministic,
	// collision-resistant, DNS-1123 label value (see ownerLabelValue).
	ownerLabelKey = "dev.box/owner"

	// boxNamePrefix is the fixed prefix of deterministic persistent pod names.
	boxNamePrefix = "box-"

	// userIdentitySep separates the identity inputs when deriving pod names, so
	// that (owner, template) pairs cannot bleed into each other.
	userIdentitySep = "\x00"

	// capListTimeout bounds the pod-listing call inside a config request. The
	// request itself is shorter-lived than the overall auth timeout, and a
	// single List call is fast; a slow API simply trips this and denies anyway.
	capListTimeout = 5 * time.Second

	// limitLabelValue is a K8s label value's maximum length.
	limitLabelValue = 63
)

var (
	// errEmptyAuthenticatedIdentity denies persistent requests that cannot be
	// tied to a verified owner.
	errEmptyAuthenticatedIdentity = errors.New("no authenticated identity available")
	// errPodCapReached intentionally stays generic. ContainerSSH maps webhook
	// failures to its standard fail-closed client message; readable cap errors
	// are tracked separately.
	errPodCapReached = errors.New("persistent pod cap reached")
)

// persistentBoxConfig groups the policy and Kubernetes dependency used only
// while the server is operating in persistent mode.
type persistentBoxConfig struct {
	operatingMode config.KubernetesExecutionMode
	namespace     string
	maxPods       int
	pods          PodLister
}

func (c persistentBoxConfig) enabled() bool {
	return c.operatingMode == config.KubernetesExecutionModePersistent
}

// PodLister returns live pod names for one owner in a namespace. It is the only
// Kubernetes dependency the config server has (RBAC: list pods in the session
// namespace). The owner label value passed in is already Kubernetes-valid.
type PodLister interface {
	LivePodNamesForOwner(ctx context.Context, namespace, ownerLabel string) ([]string, error)
}

// kubePodLister lists pods through the in-cluster Kubernetes API.
type kubePodLister struct {
	client kubernetes.Interface
}

// LivePodNamesForOwner returns non-terminating pods that have not reached a
// terminal phase. Pending, Running and Unknown pods still occupy a live box.
func (l *kubePodLister) LivePodNamesForOwner(ctx context.Context, namespace, ownerLabel string) ([]string, error) {
	pods, err := l.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: ownerLabelKey + "=" + ownerLabel,
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		names = append(names, pod.Name)
	}
	return names, nil
}

// newKubePodLister builds a PodLister from the in-cluster service account.
func newKubePodLister() (PodLister, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("cannot build in-cluster Kubernetes config for pod listing: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot build Kubernetes client for pod listing: %w", err)
	}
	return &kubePodLister{client: client}, nil
}

// applyPersistent derives the box identity from the authenticated username and
// resolved template, enforces the cap, and injects the persistent-only fields.
func (h *configHandler) applyPersistent(req config.Request, cfg config.AppConfig, templateName string) (config.AppConfig, error) {
	owner := strings.TrimSpace(req.AuthenticatedUsername)
	if owner == "" {
		h.logger.WithLabel("username", message.LabelValue(req.Username)).
			Error(message.NewMessage(
				logCodePersistentDenied,
				"Denying connection: no authenticated identity available (authenticatedUsername is empty)",
			))
		return config.AppConfig{}, errEmptyAuthenticatedIdentity
	}

	podName := boxPodName(owner, templateName)
	if err := h.enforceCap(owner, podName); err != nil {
		h.logger.WithLabel("username", message.LabelValue(req.Username)).
			WithLabel("owner", message.LabelValue(owner)).
			Error(message.NewMessage(
				logCodePersistentDenied,
				"Denying connection for owner %s: %v", owner, err,
			))
		return config.AppConfig{}, err
	}

	cfg.Kubernetes.Pod.Metadata.Name = podName
	// Always include this in persistent responses. The chart omits the base value
	// for mixed-mode templates because mergo cannot override true with false;
	// request-level injection keeps persistent boxes on-demand in that case.
	cfg.Kubernetes.Pod.CreateMissingPods = true
	labels := make(map[string]string, len(cfg.Kubernetes.Pod.Metadata.Labels)+1)
	for key, value := range cfg.Kubernetes.Pod.Metadata.Labels {
		labels[key] = value
	}
	labels[ownerLabelKey] = ownerLabelValue(owner)
	cfg.Kubernetes.Pod.Metadata.Labels = labels

	h.logger.WithLabel("username", message.LabelValue(req.Username)).
		WithLabel("owner", message.LabelValue(owner)).
		WithLabel("template", message.LabelValue(templateName)).
		WithLabel("podName", message.LabelValue(podName)).
		Debug(message.NewMessage(
			logCodePersistentInjected,
			"Injected persistent pod name %s (owner %s, template %s)",
			podName, owner, templateName,
		))
	return cfg, nil
}

// templateSelectsNonPersistent reports whether the template explicitly
// overrides the execution mode to connection or session mode.
func templateSelectsNonPersistent(cfg config.AppConfig) bool {
	return cfg.Kubernetes.Pod.Mode != "" &&
		cfg.Kubernetes.Pod.Mode != config.KubernetesExecutionModePersistent
}

// enforceCap denies when the target pod does not exist and the owner is already
// at the per-user cap. Reconnects always pass. Listing failures deny.
func (h *configHandler) enforceCap(owner, podName string) error {
	if h.boxes.maxPods <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), capListTimeout)
	defer cancel()

	names, err := h.boxes.pods.LivePodNamesForOwner(ctx, h.boxes.namespace, ownerLabelValue(owner))
	if err != nil {
		return fmt.Errorf(
			"failed to list pods owned by %s in namespace %s: %w",
			owner, h.boxes.namespace, err,
		)
	}
	for _, name := range names {
		if name == podName {
			return nil
		}
	}
	if len(names) >= h.boxes.maxPods {
		return errPodCapReached
	}
	return nil
}

// boxPodName derives "box-" plus the first 10 hexadecimal SHA-256 characters
// for an (owner, template) pair. It is DNS-1123-safe and contains no PII.
func boxPodName(owner, template string) string {
	return boxNamePrefix + shortHash(owner+userIdentitySep+template, 10)
}

func shortHash(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:length]
}

// ownerLabelValue returns a valid, deterministic owner label. Already-valid
// usernames remain human-readable; lossy sanitization gets a hash suffix so
// distinct authenticated identities cannot share a cap quota.
func ownerLabelValue(owner string) string {
	if isValidLabelValue(owner) {
		return owner
	}

	var b strings.Builder
	prevDash := false
	for i := 0; i < len(owner); i++ {
		c := owner[i]
		switch {
		case isAlnum(c), c == '_', c == '.':
			b.WriteByte(c)
			prevDash = false
		case c == '-':
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	core := strings.Trim(b.String(), "-")

	suffix := "-" + shortHash(owner, 8)
	maxCore := limitLabelValue - len(suffix)
	if len(core) > maxCore {
		core = strings.TrimRight(core[:maxCore], "-")
	}
	if core == "" {
		core = "owner"
	}
	return core + suffix
}

// isValidLabelValue reports whether s is already a legal Kubernetes label value.
func isValidLabelValue(s string) bool {
	if len(s) == 0 || len(s) > limitLabelValue {
		return false
	}
	if !isAlnum(s[0]) || !isAlnum(s[len(s)-1]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch {
		case isAlnum(s[i]), s[i] == '-', s[i] == '_', s[i] == '.':
		default:
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
