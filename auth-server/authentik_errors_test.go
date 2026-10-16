package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type unavailableTransport struct {
	cause error
}

func (t unavailableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.cause
}

func TestLookupTransportErrorOmitsPublicKeyURL(t *testing.T) {
	cause := errors.New("fixture transport unavailable")
	client := &authentikClient{cfg: authentikConfig{
		BaseURL:    "https://authentik.example.test",
		HTTPClient: &http.Client{Transport: unavailableTransport{cause: cause}},
	}}
	_, err := client.lookupUser(context.Background(), "ssh-ed25519 SECRET-KEY-MATERIAL")
	if !errors.Is(err, cause) {
		t.Fatalf("expected underlying transport cause, got %v", err)
	}
	for _, sensitive := range []string{"SECRET-KEY-MATERIAL", "ssh-ed25519", "attributes=", "https://"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("transport error exposes request URL/key: %v", err)
		}
	}
}
