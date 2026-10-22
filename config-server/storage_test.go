package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.containerssh.io/containerssh/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const storageTemplate = `volumeClaimTemplates:
  - metadata:
      name: home
    spec:
      accessModes: [ReadWriteOnce]
      storageClassName: csi-cinder
      resources:
        requests:
          storage: 25Gi
kubernetes:
  pod:
    spec:
      securityContext:
        fsGroup: 1000
      volumes:
        - name: home
          persistentVolumeClaim:
            claimName: home
        - name: shared
          persistentVolumeClaim:
            claimName: operator-shared
      containers:
        - name: shell
          env: [{name: HOME, value: /home}]
          volumeMounts: [{name: home, mountPath: /home}]
`

func storageHandler(t *testing.T) *configHandler {
	t.Helper()
	return testHandler(t, func(h *configHandler) {
		h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": storageTemplate})
	})
}

func TestStorageCreatesRetainedPerBoxClaim(t *testing.T) {
	h := storageHandler(t)
	cfg, err := h.OnConfig(configRequest("ubuntu", " alice "))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubernetes.Pod.Metadata.Labels["dev.box/template"] != "ubuntu" {
		t.Fatal("pod is missing the claim's template query label")
	}
	name := cfg.Kubernetes.Pod.Metadata.Name + "-home"
	volumes := cfg.Kubernetes.Pod.Spec.Volumes
	if volumes[0].PersistentVolumeClaim.ClaimName != name {
		t.Fatalf("managed reference = %q, want %q", volumes[0].PersistentVolumeClaim.ClaimName, name)
	}
	if volumes[1].PersistentVolumeClaim.ClaimName != "operator-shared" {
		t.Fatal("unmanaged claim reference rewritten")
	}
	claim, err := h.kube.CoreV1().PersistentVolumeClaims(h.boxes.namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Resources.Requests.Storage().String() != "25Gi" || claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "csi-cinder" {
		t.Fatalf("native claim spec lost: %+v", claim.Spec)
	}
	if len(claim.OwnerReferences) != 0 {
		t.Fatal("retained claim has owner references")
	}
	if claim.Labels["dev.box/owner"] != "alice" || claim.Labels["dev.box/template"] != "ubuntu" || claim.Labels["dev.box/claim"] != "home" {
		t.Fatalf("missing query labels: %v", claim.Labels)
	}
	if claim.Annotations["dev.box/owner"] != "alice" || claim.Annotations["dev.box/template"] != "ubuntu" || claim.Annotations["dev.box/claim"] != "home" {
		t.Fatalf("missing exact identity: %v", claim.Annotations)
	}
	if cfg.Kubernetes.Pod.Spec.SecurityContext.FSGroup == nil || *cfg.Kubernetes.Pod.Spec.SecurityContext.FSGroup != 1000 {
		t.Fatal("authored fsGroup lost")
	}
}

func TestStorageCachedSpecIsolatedAcrossOwners(t *testing.T) {
	h := storageHandler(t)
	check := func(owner string) error {
		cfg, err := h.OnConfig(configRequest("ubuntu", owner))
		if err != nil {
			return err
		}
		if got, want := cfg.Kubernetes.Pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName, cfg.Kubernetes.Pod.Metadata.Name+"-home"; got != want {
			return fmt.Errorf("%s reference %s, want %s", owner, got, want)
		}
		claim, err := h.kube.CoreV1().PersistentVolumeClaims(h.boxes.namespace).Get(context.Background(), cfg.Kubernetes.Pod.Metadata.Name+"-home", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if claim.Annotations["dev.box/owner"] != owner {
			return fmt.Errorf("%s received foreign claim", owner)
		}
		// Deliberately mutate the returned spec; cached and in-flight specs are independent.
		cfg.Kubernetes.Pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "caller-mutation"
		return nil
	}
	for _, owner := range []string{"alice", "bob", "alice"} {
		if err := check(owner); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := []string{"alice", "bob"}[i%2]
			if err := check(owner); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

func TestStorageRejectsForeignAndTerminatingClaims(t *testing.T) {
	cases := map[string]func(*corev1.PersistentVolumeClaim){
		"owner collision":    func(c *corev1.PersistentVolumeClaim) { c.Annotations["dev.box/owner"] = "other" },
		"template collision": func(c *corev1.PersistentVolumeClaim) { c.Annotations["dev.box/template"] = "other" },
		"claim collision":    func(c *corev1.PersistentVolumeClaim) { c.Annotations["dev.box/claim"] = "other" },
		"missing identity":   func(c *corev1.PersistentVolumeClaim) { c.Annotations = nil },
		"wrong labels":       func(c *corev1.PersistentVolumeClaim) { c.Labels["dev.box/owner"] = "other" },
		"terminating":        func(c *corev1.PersistentVolumeClaim) { now := metav1.Now(); c.DeletionTimestamp = &now },
		"garbage collected": func(c *corev1.PersistentVolumeClaim) {
			c.OwnerReferences = []metav1.OwnerReference{{Name: "pod", UID: "pod-uid"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := storageHandler(t)
			cfg, err := h.OnConfig(configRequest("ubuntu", "alice"))
			if err != nil {
				t.Fatal(err)
			}
			claims := h.kube.CoreV1().PersistentVolumeClaims(h.boxes.namespace)
			claim, err := claims.Get(context.Background(), cfg.Kubernetes.Pod.Metadata.Name+"-home", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mutate(claim)
			if _, err = claims.Update(context.Background(), claim, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			denied, err := h.OnConfig(configRequest("ubuntu", "alice"))
			if err == nil || !reflect.DeepEqual(denied, config.AppConfig{}) {
				t.Fatalf("unsafe claim accepted: %v", err)
			}
		})
	}
}

func TestStorageAlreadyExistsRaceVerifiesIdentity(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			h := storageHandler(t)
			client := h.kube.(*fake.Clientset)
			client.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
				claim := action.(ktesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
				if foreign {
					claim.Annotations["dev.box/owner"] = "foreign"
				}
				if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"), claim, h.boxes.namespace); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "persistentvolumeclaims"}, claim.Name)
			})
			_, err := h.OnConfig(configRequest("ubuntu", "alice"))
			if (err != nil) != foreign {
				t.Fatalf("foreign=%t error=%v", foreign, err)
			}
		})
	}
}

func TestStorageValidatesMountedTemplatesBeforeAPIAccess(t *testing.T) {
	for name, body := range map[string]string{
		"connection override": strings.Replace(storageTemplate, "  pod:\n", "  pod:\n    mode: connection\n", 1),
		"session override":    strings.Replace(storageTemplate, "  pod:\n", "  pod:\n    mode: session\n", 1),
		"unknown mode":        strings.Replace(storageTemplate, "  pod:\n", "  pod:\n    mode: invalid\n", 1),
		"missing size":        strings.Replace(storageTemplate, "storage: 25Gi", "cpu: 1", 1),
		"zero size":           strings.Replace(storageTemplate, "storage: 25Gi", "storage: 0", 1),
		"negative size":       strings.Replace(storageTemplate, "storage: 25Gi", "storage: -1Gi", 1),
		"bad size":            strings.Replace(storageTemplate, "storage: 25Gi", "storage: invalid", 1),
		"bad name":            strings.Replace(storageTemplate, "name: home", "name: BAD_NAME", 1),
		"duplicate claim":     strings.Replace(storageTemplate, "kubernetes:", "  - metadata: {name: home}\n    spec: {resources: {requests: {storage: 1Gi}}}\nkubernetes:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			h := storageHandler(t)
			h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": body})
			if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
				t.Fatal("invalid managed storage accepted")
			}
			if actions := h.kube.(*fake.Clientset).Actions(); len(actions) != 0 {
				t.Fatalf("validation performed API operations: %v", actions)
			}
		})
	}
	for _, mode := range []config.KubernetesExecutionMode{config.KubernetesExecutionModeConnection, config.KubernetesExecutionModeSession} {
		t.Run(string(mode), func(t *testing.T) {
			h := storageHandler(t)
			h.boxes.operatingMode = mode
			if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
				t.Fatal("nonpersistent storage accepted")
			}
		})
	}
}

func TestStorageCapDenialCreatesNoClaims(t *testing.T) {
	for name, identity := range map[string]string{"cap": "alice", "blank identity": " "} {
		t.Run(name, func(t *testing.T) {
			h := storageHandler(t)
			h.boxes.pods = &fakeLister{names: func(context.Context, string, string) ([]string, error) { return []string{"one", "two", "three"}, nil }}
			if _, err := h.OnConfig(configRequest("ubuntu", identity)); err == nil {
				t.Fatal("request accepted")
			}
			if actions := h.kube.(*fake.Clientset).Actions(); len(actions) != 0 {
				t.Fatalf("claim API called before cap/identity denial: %v", actions)
			}
		})
	}
}

func TestStoragePartialFailureRetainsAndReusesClaims(t *testing.T) {
	h := storageHandler(t)
	h.dir = testTemplateDir(t, map[string]string{"ubuntu.yaml": strings.Replace(storageTemplate, "kubernetes:", "  - metadata: {name: work}\n    spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}\nkubernetes:", 1)})
	client := h.kube.(*fake.Clientset)
	fail := true
	client.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		claim := action.(ktesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim)
		if fail && strings.HasSuffix(claim.Name, "-work") {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, claim.Name, errors.New("quota exceeded"))
		}
		return false, nil, nil
	})
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err == nil {
		t.Fatal("partial failure accepted")
	}
	home, err := client.CoreV1().PersistentVolumeClaims(h.boxes.namespace).Get(context.Background(), "box-ec061c6832-home", metav1.GetOptions{})
	if err != nil {
		t.Fatal("first claim was rolled back:", err)
	}
	fail = false
	if _, err := h.OnConfig(configRequest("ubuntu", "alice")); err != nil {
		t.Fatal(err)
	}
	retained, err := client.CoreV1().PersistentVolumeClaims(h.boxes.namespace).Get(context.Background(), home.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(home, retained) {
		t.Fatal("retained claim reconciled unexpectedly")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "persistentvolumeclaims" && action.GetVerb() != "get" && action.GetVerb() != "create" {
			t.Fatalf("unexpected claim operation: %s", action.GetVerb())
		}
	}
}

func TestStorageAPIErrorsFailClosed(t *testing.T) {
	for _, verb := range []string{"get", "create"} {
		t.Run(verb, func(t *testing.T) {
			h := storageHandler(t)
			h.kube.(*fake.Clientset).PrependReactor(verb, "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API unavailable") })
			cfg, err := h.OnConfig(configRequest("ubuntu", "alice"))
			if err == nil || !reflect.DeepEqual(cfg, config.AppConfig{}) {
				t.Fatalf("API error accepted: %v", err)
			}
		})
	}
}
