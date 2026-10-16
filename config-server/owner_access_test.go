package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.containerssh.io/containerssh/config"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestSettingsReconcilesTemplateAdditionsAndRemovals(t *testing.T) {
	h := settingsHandler(t)
	ctx := context.Background()
	first, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	account := first.Kubernetes.Pod.Spec.ServiceAccountName
	roles := h.kube.RbacV1().Roles(h.boxes.namespace)
	original, err := roles.Get(ctx, account, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "dev.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Explicit non-persistent applications must never enter the Role.
	if err := os.WriteFile(filepath.Join(h.dir, "temporary.yaml"), []byte("kubernetes:\n  pod:\n    mode: session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OnConfig(configRequest("settings", "alice")); err != nil {
		t.Fatal(err)
	}
	added, err := roles.Get(ctx, account, metav1.GetOptions{})
	if err != nil || len(added.Rules) != 1 || len(added.Rules[0].ResourceNames) != 2 {
		t.Fatalf("template addition was not reconciled: %+v, %v", added, err)
	}
	if err := os.Remove(filepath.Join(h.dir, "ubuntu.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.dir, "dev.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OnConfig(configRequest("settings", "alice")); err != nil {
		t.Fatal(err)
	}
	empty, err := roles.Get(ctx, account, metav1.GetOptions{})
	if err != nil || len(empty.Rules) != 0 {
		t.Fatalf("empty templates must revoke ALL rules (empty resourceNames grants every pod): %+v, %v", empty, err)
	}
	if len(original.Rules[0].ResourceNames) != 1 {
		t.Fatal("reconciliation mutated an earlier API response")
	}
	accounts, _ := h.kube.CoreV1().ServiceAccounts(h.boxes.namespace).List(ctx, metav1.ListOptions{})
	bindings, _ := h.kube.RbacV1().RoleBindings(h.boxes.namespace).List(ctx, metav1.ListOptions{})
	allRoles, _ := roles.List(ctx, metav1.ListOptions{})
	if len(accounts.Items) != 1 || len(bindings.Items) != 1 || len(allRoles.Items) != 1 {
		t.Fatal("reconnect created per-connection authorization objects")
	}
}

func TestSettingsOwnerCredentialsAreDistinctAndStable(t *testing.T) {
	h := settingsHandler(t)
	alice, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := h.OnConfig(configRequest("settings", "bob"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := alice.Kubernetes.Pod.Spec.ServiceAccountName, bob.Kubernetes.Pod.Spec.ServiceAccountName
	if a == b || a != again.Kubernetes.Pod.Spec.ServiceAccountName {
		t.Fatal("per-owner ServiceAccounts are not distinct/stable")
	}
	roles := h.kube.RbacV1().Roles(h.boxes.namespace)
	ar, _ := roles.Get(context.Background(), a, metav1.GetOptions{})
	br, _ := roles.Get(context.Background(), b, metav1.GetOptions{})
	for _, own := range ar.Rules[0].ResourceNames {
		for _, foreign := range br.Rules[0].ResourceNames {
			if own == foreign {
				t.Fatal("different owners received overlapping pod authority")
			}
		}
	}
	// Display identity is unchanged; authorization uses persistent naming's trim.
	spaced, err := h.OnConfig(configRequest("settings", " alice "))
	if err != nil || spaced.Kubernetes.Pod.Spec.ServiceAccountName != a {
		t.Fatalf("canonicalization diverged from persistent boxes: %v", err)
	}
}

func TestSettingsFailsClosedOnKubernetesReconciliationErrors(t *testing.T) {
	for _, resource := range []string{"serviceaccounts", "roles", "rolebindings"} {
		for _, verb := range []string{"get", "create", "update"} {
			t.Run(resource+"/"+verb, func(t *testing.T) {
				h := settingsHandler(t)
				client := h.kube.(*fake.Clientset)
				if verb == "update" {
					// A drifted object must be repaired, never returned as authority.
					cfg, err := h.OnConfig(configRequest("settings", "alice"))
					if err != nil {
						t.Fatal(err)
					}
					name, ns := cfg.Kubernetes.Pod.Spec.ServiceAccountName, h.boxes.namespace
					switch resource {
					case "serviceaccounts":
						obj, _ := client.CoreV1().ServiceAccounts(ns).Get(context.Background(), name, metav1.GetOptions{})
						obj.AutomountServiceAccountToken = nil
						_, _ = client.CoreV1().ServiceAccounts(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
					case "roles":
						obj, _ := client.RbacV1().Roles(ns).Get(context.Background(), name, metav1.GetOptions{})
						obj.Rules = []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"*"}}}
						_, _ = client.RbacV1().Roles(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
					case "rolebindings":
						obj, _ := client.RbacV1().RoleBindings(ns).Get(context.Background(), name, metav1.GetOptions{})
						obj.Subjects = nil
						_, _ = client.RbacV1().RoleBindings(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
					}
				}
				client.PrependReactor(verb, resource, func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("API unavailable")
				})
				cfg, err := h.OnConfig(configRequest("settings", "alice"))
				if err == nil || !reflect.DeepEqual(cfg, config.AppConfig{}) {
					t.Fatalf("API failure returned credential-bearing config: %v", err)
				}
			})
		}
	}
}

func TestSettingsRetriesConcurrentObjectCreation(t *testing.T) {
	h := settingsHandler(t)
	client := h.kube.(*fake.Clientset)
	// The typed client retries API conflicts, including concurrent first logins.
	for _, resource := range []string{"serviceaccounts", "roles", "rolebindings"} {
		attempts := 0
		client.PrependReactor("create", resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
			attempts++
			if attempts == 1 {
				return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: action.GetResource().Resource}, "owner")
			}
			return false, nil, nil
		})
	}
	if _, err := h.OnConfig(configRequest("settings", "alice")); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRepairsRoleDriftAndRetriesUpdateConflicts(t *testing.T) {
	h := settingsHandler(t)
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	roles := h.kube.RbacV1().Roles(h.boxes.namespace)
	name := cfg.Kubernetes.Pod.Spec.ServiceAccountName
	original, err := roles.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	drifted := original.DeepCopy()
	drifted.Rules = []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}
	if _, err := roles.Update(ctx, drifted, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	h.kube.(*fake.Clientset).PrependReactor("update", "roles", func(k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}, name, errors.New("concurrent update"))
		}
		return false, nil, nil
	})
	if _, err := h.OnConfig(configRequest("settings", "alice")); err != nil {
		t.Fatal(err)
	}
	repaired, err := roles.Get(ctx, name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(original.Rules, repaired.Rules) {
		t.Fatalf("role drift not repaired: %+v, %v", repaired, err)
	}
}

func TestSettingsDeniesUnexpectedBindingRoleReference(t *testing.T) {
	h := settingsHandler(t)
	cfg, err := h.OnConfig(configRequest("settings", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	bindings := h.kube.RbacV1().RoleBindings(h.boxes.namespace)
	binding, err := bindings.Get(context.Background(), cfg.Kubernetes.Pod.Spec.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	binding.RoleRef.Name = "foreign-role"
	// The fake API permits replacing immutable roleRef to simulate a preexisting
	// unexpected object. Real Kubernetes would reject that mutation.
	if _, err := bindings.Update(context.Background(), binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OnConfig(configRequest("settings", "alice")); err == nil {
		t.Fatal("returned credentials bound to a foreign role")
	}
}

func TestSettingsMalformedAuthorizationTemplatesDeny(t *testing.T) {
	h := settingsHandler(t)
	if err := os.WriteFile(filepath.Join(h.dir, "broken.yaml"), []byte("kubernetes: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OnConfig(configRequest("settings", "alice")); err == nil {
		t.Fatal("malformed authorization source accepted")
	}
}
