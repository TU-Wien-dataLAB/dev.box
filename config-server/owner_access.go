package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
)

// ensureOwnerAccess reconciles three static objects per verified owner. The
// config server holds the pod permissions it delegates, so Kubernetes' normal
// role-escalation/binding checks apply; no escalate or bind grant is needed.
// templates is the owner's current persistent box template list from the
// operator-mounted store; it defines both the Role's exact pod names and the
// settings application's trusted template list.
func (h *configHandler) ensureOwnerAccess(owner string, templates []string) (string, error) {
	if h.kube == nil || h.boxes.namespace == "" {
		return "", fmt.Errorf("settings Kubernetes authorization is not configured")
	}
	// Use the same canonicalization as persistent naming, not display sanitization.
	name := "dev-box-owner-" + shortHash(strings.TrimSpace(owner), 48)
	meta := metav1.ObjectMeta{Name: name, Namespace: h.boxes.namespace}
	var rules []rbacv1.PolicyRule
	if len(templates) > 0 {
		names := make([]string, 0, len(templates))
		for _, template := range templates {
			names = append(names, boxPodName(strings.TrimSpace(owner), template))
		}
		sort.Strings(names)
		// Empty resourceNames means ALL names in RBAC, not none. A user with
		// no persistent templates must instead receive a Role with no rules.
		rules = []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "delete"}, ResourceNames: names}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), capListTimeout)
	defer cancel()
	if err := h.ensureOwnerServiceAccount(ctx, meta); err != nil {
		return "", err
	}
	if err := h.ensureOwnerRole(ctx, meta, rules); err != nil {
		return "", err
	}
	if err := h.ensureOwnerBinding(ctx, meta); err != nil {
		return "", err
	}
	return name, nil
}

// persistentTemplates returns the operator's mounted ordinary templates that
// are eligible persistent boxes, sorted by template name. It is the single
// source for both the owner Role's exact pod names and the settings
// application's trusted template list, so the two can never diverge. Never
// include connection/session applications, settings, or client-supplied
// names. Parsing failures deny the settings request instead of retaining a
// stale template list or stale grants.
func (h *configHandler) persistentTemplates() ([]string, error) {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return nil, fmt.Errorf("read authorization templates: %w", err)
	}
	templates := []string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		filename := entry.Name()
		ext := ""
		if strings.HasSuffix(filename, ".yaml") {
			ext = ".yaml"
		} else if strings.HasSuffix(filename, ".json") {
			ext = ".json"
		} else {
			continue
		}
		template := strings.TrimSuffix(filename, ext)
		if entry.IsDir() || !validTemplateName(template) || template == settingsTemplateName || seen[template] {
			continue
		}
		seen[template] = true
		cfg, _, err := h.load(template)
		if err != nil {
			return nil, fmt.Errorf("read authorization template %q: %w", template, err)
		}
		if h.boxes.enabled() && !templateSelectsNonPersistent(cfg) {
			templates = append(templates, template)
		}
	}
	sort.Strings(templates)
	return templates, nil
}

func (h *configHandler) ensureOwnerServiceAccount(ctx context.Context, meta metav1.ObjectMeta) error {
	accounts := h.kube.CoreV1().ServiceAccounts(meta.Namespace)
	return retry.OnError(retry.DefaultRetry, reconcileConflict, func() error {
		current, err := accounts.Get(ctx, meta.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = accounts.Create(ctx, &corev1.ServiceAccount{ObjectMeta: meta, AutomountServiceAccountToken: ptr.To(false)}, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if current.AutomountServiceAccountToken != nil && !*current.AutomountServiceAccountToken {
			return nil
		}
		current.AutomountServiceAccountToken = ptr.To(false)
		_, err = accounts.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (h *configHandler) ensureOwnerRole(ctx context.Context, meta metav1.ObjectMeta, rules []rbacv1.PolicyRule) error {
	roles := h.kube.RbacV1().Roles(meta.Namespace)
	return retry.OnError(retry.DefaultRetry, reconcileConflict, func() error {
		current, err := roles.Get(ctx, meta.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = roles.Create(ctx, &rbacv1.Role{ObjectMeta: meta, Rules: rules}, metav1.CreateOptions{})
			return err
		}
		if err != nil || equality.Semantic.DeepEqual(current.Rules, rules) {
			return err
		}
		current.Rules = rules
		_, err = roles.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (h *configHandler) ensureOwnerBinding(ctx context.Context, meta metav1.ObjectMeta) error {
	bindings := h.kube.RbacV1().RoleBindings(meta.Namespace)
	ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: meta.Name}
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: meta.Name, Namespace: meta.Namespace}}
	return retry.OnError(retry.DefaultRetry, reconcileConflict, func() error {
		current, err := bindings.Get(ctx, meta.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = bindings.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: meta, RoleRef: ref, Subjects: subjects}, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		// RoleRef is immutable. An unexpected object must deny rather than
		// silently return a token bound to a different role.
		if current.RoleRef != ref {
			return fmt.Errorf("owner RoleBinding %s has an unexpected roleRef", meta.Name)
		}
		if equality.Semantic.DeepEqual(current.Subjects, subjects) {
			return nil
		}
		current.Subjects = subjects
		_, err = bindings.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func reconcileConflict(err error) bool {
	return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
}
