// Command auth-server is a ContainerSSH authentication (and configuration)
// server backed by an authentik [1] instance for SSH public-key lookups.
//
// It implements the ContainerSSH auth webhook protocol by building on the
// official module (go.containerssh.io/containerssh, auth/webhook and
// config/webhook packages) — the same API as
// cmd/containerssh-testauthconfigserver in the ContainerSSH repo.
//
// The server exposes four endpoints on one listener (spec §2/§6):
//
//	POST /password   username/password authentication (disabled by default)
//	POST /pubkey     SSH public key authentication — the authentik lookup
//	POST /authz      post-auth authorization (optional group gate)
//	POST /config     empty per-connection config (base config unchanged)
//
// The authentik side lives in authentik.go (API client) and auth_handler.go
// (the handler). How it works: an SSH client presents a public key; the server
// canonicalizes it (ssh.ParseAuthorizedKey) and fingerprints it
// (ssh.FingerprintSHA256), then looks the fingerprint up via
// GET /api/v3/core/users/?attributes={"ssh_key_fingerprint":"SHA256:..."} —
// an exact match on the user attribute set by the settings-flow prompt / the
// normalizing sync (sync.go). See spec §4.
//
// [1] https://goauthentik.io
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.containerssh.io/containerssh/config"
	containersshHTTP "go.containerssh.io/containerssh/http"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
	"go.containerssh.io/containerssh/service"
)

// Environment variables (see README.md for the full table).
const (
	envListen        = "CONTAINERSSH_LISTEN"
	envLogLevel      = "CONTAINERSSH_LOG_LEVEL"
	envTLSCert       = "CONTAINERSSH_TLS_CERT"
	envTLSKey        = "CONTAINERSSH_TLS_KEY"
	envTLSClientCA   = "CONTAINERSSH_TLS_CLIENTCA"
	envAuthentikURL  = "AUTHENTIK_URL"
	envAuthToken     = "AUTHENTIK_TOKEN"
	envAuthTokenFile = "AUTHENTIK_TOKEN_FILE"
	envWrToken       = "AUTHENTIK_WRITE_TOKEN"
	envWrTokenFile   = "AUTHENTIK_WRITE_TOKEN_FILE"
	envAuthentikCA   = "AUTHENTIK_CA_FILE"
	envInsecure      = "AUTHENTIK_INSECURE_SKIP_VERIFY"
	envEnforceUser   = "AUTH_SERVER_ENFORCE_USERNAME"
	envPasswordUsers = "AUTH_SERVER_PASSWORD_USERS"
	envRequireGroup  = "AUTH_SERVER_REQUIRE_GROUP"
	envSyncInterval  = "AUTH_SERVER_SYNC_INTERVAL"
	envSyncWrite     = "AUTH_SERVER_SYNC_WRITE"
)

func main() {
	listen := env(envListen, "0.0.0.0:8080")
	logger, err := log.NewLogger(config.LogConfig{
		Level:       config.LogLevel(parseInt(env(envLogLevel, "6"))),
		Format:      config.LogFormatLJSON,
		Destination: config.LogDestinationStdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// ---- authentik connection -------------------------------------------
	authenticURL := strings.TrimRight(env(envAuthentikURL, ""), "/")
	if authenticURL == "" {
		fail(logger, "AUTH_DEV_START_FAILED",
			"%s is required (e.g. https://authentik.example.com)", envAuthentikURL)
	}
	readToken, err := tokenFromEnv(logger)
	if err != nil {
		fail(logger, "AUTH_DEV_START_FAILED", "%v", err)
	}
	httpClient, err := newHTTPClient(env(envAuthentikCA, ""), envBool(envInsecure))
	if err != nil {
		fail(logger, "AUTH_DEV_START_FAILED", "invalid authentik HTTP client: %v", err)
	}

	writeToken := ""
	if v, err := secretFromEnv(envWrTokenFile, envWrToken, logger); err != nil {
		fail(logger, "AUTH_DEV_START_FAILED", "%v", err)
	} else {
		writeToken = v
	}

	authentikClient := &authentikClient{cfg: authentikConfig{
		BaseURL:    authenticURL,
		ReadToken:  readToken,
		WriteToken: writeToken,
		HTTPClient: httpClient,
	}}
	// ---- auth behaviour ---------------------------------------------------
	authCfg := authConfig{
		EnforceUsername: envBoolDefault(envEnforceUser, true),
		PasswordUsers:   parsePasswordUsers(env(envPasswordUsers, "")),
		RequireGroup:    env(envRequireGroup, ""),
	}

	// ---- server + service lifecycle --------------------------------------
	httpConfig := config.HTTPServerConfiguration{Listen: listen}
	if cert, key := env(envTLSCert, ""), env(envTLSKey, ""); cert != "" && key != "" {
		httpConfig.Cert = cert
		httpConfig.Key = key
		httpConfig.ClientCACert = env(envTLSClientCA, "")
	}

	mux, err := buildHandlers(authentikClient, authCfg, logger)
	if err != nil {
		fail(logger, "AUTH_DEV_START_FAILED", "failed to build handlers: %v", err)
	}
	srv, err := containersshHTTP.NewServer(
		"authconfig",
		httpConfig,
		mux,
		logger,
		func(url string) {
			logger.Info(message.NewMessage(
				"AUTH_DEV_AVAILABLE",
				"The authentication server is now available at %s",
				url,
			))
		},
	)
	if err != nil {
		fail(logger, "AUTH_DEV_START_FAILED", "failed to start the authentication server: %v", err)
	}
	lifecycle := service.NewLifecycle(srv)

	// ---- optional normalizing sync (spec §5.3 option (a)) -----------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if interval, ok := parseDuration(env(envSyncInterval, "")); ok && interval > 0 {
		syncWrite := envBoolDefault(envSyncWrite, true)
		if !syncWrite {
			logger.Warning(message.NewMessage(
				"AUTH_DEV_SYNC_READONLY",
				"%s=0 — the SSH key sync will only report, not write (dry-run)",
				envSyncWrite,
			))
		}
		runner := &syncRunner{
			authentik:    authentikClient,
			interval:     interval,
			writeEnabled: syncWrite,
			logger:       logger,
		}
		go runner.run(ctx)
	}

	go func() {
		if err := lifecycle.Run(); err != nil {
			logger.Critical(message.NewMessage(
				"AUTH_DEV_RUN_FAILED",
				"The authentication server terminated with an error: %v", err,
			))
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if _, ok := <-signals; ok {
			logger.Info(message.NewMessage(
				"AUTH_DEV_SHUTDOWN",
				"Shutting down the authentication server...",
			))
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer stopCancel()
			lifecycle.Stop(stopCtx)
			cancel()
		}
	}()

	lastError := lifecycle.Wait()
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	close(signals)
	cancel()

	if lastError != nil {
		logger.Critical(message.NewMessage(
			"AUTH_DEV_EXIT_ERROR",
			"An error happened while running the authentication server (%v)",
			lastError,
		))
		os.Exit(1)
	}
}

// tokenFromEnv resolves the read token from AUTHENTIK_TOKEN or AUTHENTIK_TOKEN_FILE.
func tokenFromEnv(logger log.Logger) (string, error) {
	token, err := secretFromEnv(envAuthTokenFile, envAuthToken, logger)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("either %s or %s must be set (authentik service-account token with read access to users)", envAuthTokenFile, envAuthToken)
	}
	return token, nil
}

// secretFromEnv reads a secret from <file> if set, else from <direct>.
func secretFromEnv(file, direct string, logger log.Logger) (string, error) {
	logger.Debug(message.NewMessage(
		"AUTH_DEV_ENV",
		"Reading secret for %s / %s", file, direct,
	))
	if v := env(file, ""); v != "" {
		data, err := os.ReadFile(v)
		if err != nil {
			return "", fmt.Errorf("failed to read %s=%s: %w", file, v, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return env(direct, ""), nil
}

// parsePasswordUsers splits the comma-separated allowlist into a set.
func parsePasswordUsers(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			set[u] = struct{}{}
		}
	}
	return set
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envBool(key string) bool { return envBoolDefault(key, false) }

func envBoolDefault(key string, def bool) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parseInt(s string) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		// default to info
		return 6
	}
	return n
}

// parseDuration parses a Go duration string; ok=false for empty/zero/unparseable.
func parseDuration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

func fail(logger log.Logger, code, format string, args ...interface{}) {
	logger.Critical(message.NewMessage(code, format, args...))
	os.Exit(1)
}
