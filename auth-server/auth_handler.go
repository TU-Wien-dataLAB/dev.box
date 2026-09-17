package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	goHttp "net/http"

	"go.containerssh.io/containerssh/auth"
	authWebhook "go.containerssh.io/containerssh/auth/webhook"
	"go.containerssh.io/containerssh/config"
	configWebhook "go.containerssh.io/containerssh/config/webhook"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
	"go.containerssh.io/containerssh/metadata"

	"golang.org/x/crypto/ssh"
)

// Log message codes for auth decisions (syslog level = log severity).
const (
	logCodePubKeySuccess = "AUTH_SERVER_PUBKEY_SUCCESS"
	logCodePubKeyDenied  = "AUTH_SERVER_PUBKEY_DENIED"
	logCodePubKeyError   = "AUTH_SERVER_PUBKEY_ERROR"
	logCodePassDenied    = "AUTH_SERVER_PASSWORD_DENIED"
	logCodePassSuccess   = "AUTH_SERVER_PASSWORD_SUCCESS"
	logCodeAuthzDenied   = "AUTH_SERVER_AUTHZ_DENIED"
	logCodeAuthzError    = "AUTH_SERVER_AUTHZ_ERROR"
)

// authConfig carries the runtime behaviour switches for the auth handler,
// all derived from environment variables in main.go.
type authConfig struct {
	// EnforceUsername requires the SSH username to equal the authentik username
	// that owns the presented key (spec §4, step 4: "verify/enforce username
	// binding"). Off = any username is accepted as long as a key is enrolled,
	// and the session is authenticated as the key's owner.
	EnforceUsername bool
	// PasswordUsers lists the usernames allowed to log in with *any* password.
	// Intended for local break-glass / test setups only — passwords are not
	// verified against authentik here. Empty = password auth disabled.
	PasswordUsers map[string]struct{}
	// RequireGroup, when set, makes OnAuthorization demand group membership.
	RequireGroup string
}

// authHandler implements auth.Handler (via auth/webhook): /password, /pubkey,
// /authz. OnPubKey is the authentik-backed SSH key lookup (spec §4).
type authHandler struct {
	authentik *authentikClient
	cfg       authConfig
	logger    log.Logger
}

// OnPassword is implemented for protocol completeness and as a constrained
// test/fallback escape hatch. This setup is key-first — by default every
// username is denied here so only the authentik-backed pubkey path grants
// access.
func (h *authHandler) OnPassword(
	meta metadata.ConnectionAuthPendingMetadata,
	password []byte,
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
	_, allowed := h.cfg.PasswordUsers[meta.Username]
	if !allowed {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			Debug(message.NewMessage(
				logCodePassDenied,
				"Password authentication denied for user %s (not in allowlist)",
				meta.Username,
			))
		return false, meta.AuthFailed(), nil
	}
	h.logger.WithLabel("username", message.LabelValue(meta.Username)).
		Warning(message.NewMessage(
			logCodePassSuccess,
			"Password authentication granted to %s via unverified test allowlist — NOT production mode",
			meta.Username,
		))
	return true, meta.Authenticated(meta.Username), nil
}

// OnPubKey authenticates an SSH public key by looking it up in authentik (spec
// §4). The lookup attribute is configurable (AUTH_SERVER_KEY_ATTRIBUTE):
//
//  1. canonicalize   ssh.ParseAuthorizedKey + ssh.FingerprintSHA256
//  2. lookup         by attributes.<keyAttribute>:
//     ssh_key_fingerprint (default) → exact "SHA256:..." filter;
//     e.g. sshPublicKey → one exact filter for a single-element list
//     containing the canonical key (no comment); no directory scan
//  3. decide         0 users -> deny; exactly 1 -> verify username binding;
//     >1 -> integrity error (server-side, 500)
//  4. infra errors   -> return err so ContainerSSH replies 500 and retries
func (h *authHandler) OnPubKey(
	meta metadata.ConnectionAuthPendingMetadata,
	publicKey auth.PublicKey,
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
	fingerprint, canonicalKey, err := fingerprintAndCanonicalKey(publicKey.PublicKey)
	if err != nil {
		// Malformed key blob — a client/UI problem, not an infrastructure one:
		// deny cleanly without a 500.
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: could not parse the key: %v",
				meta.Username, err,
			))
		return false, meta.AuthFailed(), nil
	}

	start := time.Now()
	user, err := h.authentik.lookupUser(context.Background(), fingerprint, canonicalKey)
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
			Error(message.NewMessage(
				logCodePubKeyError,
				"Public key authentication failed for %s: authentik lookup error: %v",
				meta.Username, err,
			))
		return false, meta.AuthFailed(), err
	}
	if user == nil {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("fingerprint", message.LabelValue(fingerprint)).
			WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: fingerprint %s is not enrolled in authentik",
				meta.Username, fingerprint,
			))
		return false, meta.AuthFailed(), nil
	}
	if !user.IsActive {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("owner", message.LabelValue(user.Username)).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: owner account %s is inactive",
				meta.Username, user.Username,
			))
		return false, meta.AuthFailed(), nil
	}
	if h.cfg.EnforceUsername && !strings.EqualFold(user.Username, meta.Username) {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("owner", message.LabelValue(user.Username)).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: key belongs to %s (username binding enforced)",
				meta.Username, user.Username,
			))
		return false, meta.AuthFailed(), nil
	}

	h.logger.WithLabel("username", message.LabelValue(meta.Username)).
		WithLabel("owner", message.LabelValue(user.Username)).
		WithLabel("fingerprint", message.LabelValue(fingerprint)).
		WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
		Info(message.NewMessage(
			logCodePubKeySuccess,
			"Public key authentication succeeded for %s (owner: %s, fingerprint %s)",
			meta.Username, user.Username, fingerprint,
		))
	// Authenticate as the real authentik owner, so the container runs under
	// the verified identity even when username binding is relaxed.
	return true, meta.Authenticated(user.Username), nil
}

// OnAuthorization runs after a successful authentication. By default it allows
// everyone through; with AUTH_SERVER_REQUIRE_GROUP set it gates access on
// membership of that authentik group (spec §4, step 6).
func (h *authHandler) OnAuthorization(
	meta metadata.ConnectionAuthenticatedMetadata,
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
	if h.cfg.RequireGroup == "" {
		return true, meta, nil
	}
	member, err := h.authentik.userInGroup(context.Background(), meta.AuthenticatedUsername, h.cfg.RequireGroup)
	if err != nil {
		h.logger.WithLabel("username", message.LabelValue(meta.AuthenticatedUsername)).
			Error(message.NewMessage(
				logCodeAuthzError,
				"Authorization failed for %s: group lookup error: %v",
				meta.AuthenticatedUsername, err,
			))
		return false, meta, err
	}
	if !member {
		h.logger.WithLabel("username", message.LabelValue(meta.AuthenticatedUsername)).
			WithLabel("group", message.LabelValue(h.cfg.RequireGroup)).
			Debug(message.NewMessage(
				logCodeAuthzDenied,
				"Authorization denied for %s: not a member of group %s",
				meta.AuthenticatedUsername, h.cfg.RequireGroup,
			))
		return false, meta, nil
	}
	return true, meta, nil
}

// fingerprintFromAuthorizedKey parses an armored SSH public key and returns
// (a) its canonical SHA256 fingerprint and (b) the re-marshaled key without any
// comment/whitespace — the same canonical form used for the reverse index.
func fingerprintFromAuthorizedKey(authorized string) (string, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(key), nil
}

// fingerprintAndCanonicalKey returns the fingerprint plus the canonical armored
// key line (no comment, trailing whitespace trimmed) for the sync job.
func fingerprintAndCanonicalKey(authorized string) (fingerprint, canonical string, err error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return "", "", err
	}
	return ssh.FingerprintSHA256(key),
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		nil
}

// configHandler implements config.RequestHandler. This auth/config server does
// not own per-user pod templates (that is config-server's job) — it always
// returns an empty AppConfig so ContainerSSH runs its base config unchanged.
type configHandler struct{}

func (c *configHandler) OnConfig(request config.Request) (config.AppConfig, error) {
	return config.AppConfig{}, nil
}

// httpHandler is the shared mux exposing all four ContainerSSH webhook
// endpoints on one server (like cmd/containerssh-testauthconfigserver).
type httpHandler struct {
	auth   goHttp.Handler
	config goHttp.Handler
	logger log.Logger
}

func (h *httpHandler) ServeHTTP(writer goHttp.ResponseWriter, request *goHttp.Request) {
	if request.Method != "POST" {
		writer.WriteHeader(405)
		return
	}
	switch request.URL.Path {
	case "/password", "/pubkey", "/authz":
		h.auth.ServeHTTP(writer, request)
	case "/config":
		h.config.ServeHTTP(writer, request)
	default:
		writer.WriteHeader(404)
	}
}

// buildHandlers wires the auth + config handlers over the ContainerSSH webhook
// libraries and returns the combined http.Handler for the four paths.
func buildHandlers(
	authentikClient *authentikClient,
	cfg authConfig,
	logger log.Logger,
) (goHttp.Handler, error) {
	authHTTP := authWebhook.NewHandler(
		&authHandler{authentik: authentikClient, cfg: cfg, logger: logger},
		logger,
	)
	configHTTP, err := configWebhook.NewHandler(&configHandler{}, logger)
	if err != nil {
		return nil, err
	}
	return &httpHandler{
		auth:   authHTTP,
		config: configHTTP,
		logger: logger,
	}, nil
}
