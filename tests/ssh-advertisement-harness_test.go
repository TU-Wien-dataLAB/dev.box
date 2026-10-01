// This dev.box integration test is copied into the patched upstream source's
// internal/authintegration package by containerssh-server-regression.sh.
package authintegration_test

import (
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/internal/auth"
	"go.containerssh.io/containerssh/internal/authintegration"
	"go.containerssh.io/containerssh/internal/geoip/dummy"
	"go.containerssh.io/containerssh/internal/metrics"
	"go.containerssh.io/containerssh/internal/sshserver"
	"go.containerssh.io/containerssh/log"
	"golang.org/x/crypto/ssh"
)

func TestDevBoxAdvertisementHarness(t *testing.T) {
	key, signer := fixtureKey(t)
	unenrolled, _ := fixtureKey(t)
	logger := log.NewTestLogger(t)
	webhook := httptest.NewServer(auth.NewHandler(&advertisementAuthHandler{
		key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
	}, logger))
	t.Cleanup(webhook.Close)
	webhookConfig := config.AuthWebhookClientConfig{
		HTTPClientConfiguration: config.HTTPClientConfiguration{URL: webhook.URL, Timeout: time.Second},
		AuthTimeout:             time.Second,
	}
	handler, _, err := authintegration.New(config.AuthConfig{
		PublicKeyAuth: config.PublicKeyAuthConfig{Method: config.PubKeyAuthMethodWebhook, Webhook: webhookConfig},
		Authz:         config.AuthzConfig{Method: config.AuthzMethodWebhook, Webhook: webhookConfig},
	}, sshserver.NewTestHandler(), logger, metrics.New(dummy.New()), authintegration.BehaviorNoPassthrough)
	require.NoError(t, err)
	srv := sshserver.NewTestServer(t, handler, logger, nil)
	srv.Start()
	hostKey, err := ssh.ParsePrivateKey([]byte(srv.GetHostKey()))
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(srv.GetListen())
	require.NoError(t, err)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, []byte("["+host+"]:"+port+" "+string(ssh.MarshalAuthorizedKey(hostKey.PublicKey()))), 0600))
	cmd := exec.Command("python3", os.Getenv("SSH_ADVERTISEMENT_SCRIPT"), "--host", host, "--port", port,
		"--key", key, "--unenrolled-key", unenrolled, "--known-hosts", knownHosts, "--known-template", "configured", "--unknown-template", "unknown")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "PASS: unknown template denied")
	require.Contains(t, string(output), "PASS: unenrolled key denied")
	require.Contains(t, string(output), "PASS: enrolled key reconnects")
}
