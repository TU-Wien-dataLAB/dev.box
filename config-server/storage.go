package main

import (
	"context"
	"fmt"
	"strings"

	"go.containerssh.io/containerssh/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	kubeyaml "sigs.k8s.io/yaml"
)

const (
	templateLabelKey = "dev.box/template"
	claimLabelKey    = "dev.box/claim"
)

// podTemplate is a server-owned envelope, never serialized into the webhook
// response. Native PVCs are decoded using Kubernetes JSON tags, like PodSpec.
// Its cached config and claim templates are immutable after parsing.
type podTemplate struct {
	config.AppConfig
	VolumeClaimTemplates []corev1.PersistentVolumeClaim `json:"volumeClaimTemplates"`
}

func (t *podTemplate) readClaims(data []byte, persistent bool) error {
	var envelope struct {
		Claims []corev1.PersistentVolumeClaim `json:"volumeClaimTemplates"`
	}
	if err := kubeyaml.Unmarshal(data, &envelope); err != nil {
		return err
	}
	t.VolumeClaimTemplates = envelope.Claims
	if len(t.VolumeClaimTemplates) == 0 {
		return nil
	}
	if !persistent || templateSelectsNonPersistent(t.AppConfig) {
		return fmt.Errorf("volumeClaimTemplates requires persistent mode (including the template mode)")
	}
	seen := map[string]bool{}
	for _, claim := range t.VolumeClaimTemplates {
		if problems := validation.IsDNS1123Label(claim.Name); len(problems) != 0 {
			return fmt.Errorf("invalid claim template name %q: %s", claim.Name, strings.Join(problems, "; "))
		}
		if seen[claim.Name] {
			return fmt.Errorf("duplicate claim template %q", claim.Name)
		}
		seen[claim.Name] = true
		size, ok := claim.Spec.Resources.Requests[corev1.ResourceStorage]
		if !ok || size.Sign() <= 0 {
			return fmt.Errorf("claim template %q requires an explicit positive storage request", claim.Name)
		}
	}
	return nil
}

// applyStorage runs only after applyPersistent has verified identity and cap.
// Deep-copy the pod spec before rewriting: a shallow AppConfig copy still shares
// the cached volume slice and PVC pointers with all subsequent owners.
func (h *configHandler) applyStorage(req config.Request, template podTemplate, templateName string) (config.AppConfig, error) {
	cfg := template.AppConfig
	if h.kube == nil || h.boxes.namespace == "" {
		return config.AppConfig{}, fmt.Errorf("managed claim Kubernetes client is not configured")
	}
	cfg.Kubernetes.Pod.Spec = *cfg.Kubernetes.Pod.Spec.DeepCopy()
	owner := strings.TrimSpace(req.AuthenticatedUsername)
	ctx, cancel := context.WithTimeout(context.Background(), capListTimeout)
	defer cancel()
	names := map[string]string{}
	for _, claim := range template.VolumeClaimTemplates {
		name := cfg.Kubernetes.Pod.Metadata.Name + "-" + claim.Name
		if err := h.ensureClaim(ctx, claim, name, owner, templateName); err != nil {
			return config.AppConfig{}, fmt.Errorf("retained claim %s: %w", name, err)
		}
		names[claim.Name] = name
	}
	for i := range cfg.Kubernetes.Pod.Spec.Volumes {
		ref := cfg.Kubernetes.Pod.Spec.Volumes[i].PersistentVolumeClaim
		if ref != nil {
			if name, ok := names[ref.ClaimName]; ok {
				ref.ClaimName = name
			}
		}
	}
	return cfg, nil
}

// ensureClaim creates independently retained claims. Existing specifications
// are deliberately not reconciled; identity and non-termination are required.
// No delete/rollback path exists, even after partial multi-claim failure.
func (h *configHandler) ensureClaim(ctx context.Context, template corev1.PersistentVolumeClaim, name, owner, templateName string) error {
	claims := h.kube.CoreV1().PersistentVolumeClaims(h.boxes.namespace)
	identity := map[string]string{ownerLabelKey: owner, templateLabelKey: templateName, claimLabelKey: template.Name}
	labels := map[string]string{ownerLabelKey: ownerLabelValue(owner), templateLabelKey: ownerLabelValue(templateName), claimLabelKey: template.Name}
	current, err := claims.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Construct fresh metadata: never inherit owner references, namespace, UID or
		// server fields from a declaration. Operator labels/annotations are retained.
		desired := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.boxes.namespace}, Spec: *template.Spec.DeepCopy()}
		desired.Labels = make(map[string]string, len(template.Labels)+len(labels))
		for k, v := range template.Labels {
			desired.Labels[k] = v
		}
		for k, v := range labels {
			desired.Labels[k] = v
		}
		desired.Annotations = make(map[string]string, len(template.Annotations)+len(identity))
		for k, v := range template.Annotations {
			desired.Annotations[k] = v
		}
		for k, v := range identity {
			desired.Annotations[k] = v
		}
		current, err = claims.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = claims.Get(ctx, name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	if current.DeletionTimestamp != nil {
		return fmt.Errorf("claim is terminating")
	}
	if len(current.OwnerReferences) != 0 {
		return fmt.Errorf("claim is not independently retained")
	}
	for k, v := range identity {
		if current.Annotations[k] != v || current.Labels[k] != labels[k] {
			return fmt.Errorf("claim identity mismatch for %s", k)
		}
	}
	return nil
}
