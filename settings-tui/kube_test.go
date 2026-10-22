package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIOverrideRequiresExplicitNamespace(t *testing.T) {
	t.Setenv("DEV_BOX_KUBE_API_URL", " http://127.0.0.1:1/ ")
	for _, namespace := range []string{"", " \t\n"} {
		t.Setenv("DEV_BOX_NAMESPACE", namespace)
		client, err := newBoxClient()
		if client != nil || err == nil || err.Error() != "DEV_BOX_NAMESPACE is required with DEV_BOX_KUBE_API_URL" {
			t.Fatalf("namespace %q: client = %v, error = %v", namespace, client, err)
		}
	}
	t.Setenv("DEV_BOX_NAMESPACE", " test-namespace\n")
	client, err := newBoxClient()
	if err != nil {
		t.Fatal(err)
	}
	httpClient := client.(*httpBoxClient)
	if httpClient.namespace != "test-namespace" || httpClient.base != "http://127.0.0.1:1" {
		t.Fatalf("unexpected override configuration: namespace %q, base %q", httpClient.namespace, httpClient.base)
	}
}

func TestNamespaceFromMountedFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "namespace")
	// An explicit namespace wins without even requiring a mounted file.
	t.Setenv("DEV_BOX_NAMESPACE", " explicit\n")
	if got, err := namespaceFrom(file); got != "explicit" || err != nil {
		t.Fatalf("override = %q, %v", got, err)
	}
	for _, override := range []string{"", " \t\n"} {
		t.Setenv("DEV_BOX_NAMESPACE", override)
		if err := os.WriteFile(file, []byte(" mounted-namespace\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := namespaceFrom(file); got != "mounted-namespace" || err != nil {
			t.Fatalf("mounted namespace = %q, %v", got, err)
		}
	}
	if err := os.WriteFile(file, []byte(" \t\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := namespaceFrom(file); err == nil || err.Error() != "pod namespace file is empty" {
		t.Fatalf("empty file error = %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := namespaceFrom(file); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "read pod namespace") {
		t.Fatalf("missing file error = %v", err)
	}
}
