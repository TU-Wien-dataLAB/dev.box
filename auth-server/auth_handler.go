package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.containerssh.io/containerssh/auth"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
	"go.containerssh.io/containerssh/metadata"
)

const (
	logCodePubKeySuccess = "AUTH_SERVER_PUBKEY_SUCCESS"
	logCodePubKeyDenied  = "AUTH_SERVER_PUBKEY_DENIED"
	logCodePubKeyError   = "AUTH_SERVER_PUBKEY_ERROR"
	logCodePassDenied    = "AUTH_SERVER_PASSWORD_DENIED"
	logCodeAuthzDenied   = "AUTH_SERVER_AUTHZ_DENIED"
	logCodeAuthzError    = "AUTH_SERVER_AUTHZ_ERROR"
)

// authConfig carries optional post-auth template and group policies.
type authConfig struct {
	// RequireGroup, when set, makes OnAuthorization demand membership of this
	// authentik group before the session starts.
	RequireGroup string
	// AllowedTemplates gates the client-chosen SSH username before config lookup.
	// nil disables this gate; a non-nil empty list denies every template.
	AllowedTemplates []string
}

// authDecision is the outcome of an authentication or authorization check.
//
// A policy denial may carry a user-facing message. ContainerSSH (fork PR #10)
// delivers that message to the connecting SSH client as a user authentication
// banner (RFC 4252, section 5.4) immediately before the authentication failure.
// Messages must never contain secrets. Infrastructure errors are not policy
// denials: they fail closed over HTTP 500 (see webhook.go) and must never
// produce a message.
type authDecision struct {
	success bool
	meta    metadata.ConnectionAuthenticatedMetadata
	// message is the user-facing denial message; empty on success.
	message string
}

// allow is a successful decision for meta.
func allow(meta metadata.ConnectionAuthenticatedMetadata) authDecision {
	return authDecision{success: true, meta: meta}
}

// deny is a policy denial for meta carrying a user-facing message.
func deny(meta metadata.ConnectionAuthenticatedMetadata, userMessage string) authDecision {
	return authDecision{meta: meta, message: userMessage}
}

// deny is followed by a newline so the banner renders on its own line above
// the client's final "Permission denied" error.
func userDenial(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...) + "\n"
}

// authHandler implements ContainerSSH's required authentication interface.
// Only public-key authentication can succeed.
type authHandler struct {
	authentik *authentikClient
	cfg       authConfig
	logger    log.Logger
}

// OnPassword exists because ContainerSSH's handler interface requires it.
func (h *authHandler) OnPassword(
	meta metadata.ConnectionAuthPendingMetadata,
	_ []byte,
) (authDecision, error) {
	h.logger.WithLabel("username", message.LabelValue(meta.Username)).
		Debug(message.NewMessage(
			logCodePassDenied,
			"Password authentication denied for user %s",
			meta.Username,
		))
	return deny(
		meta.AuthFailed(),
		userDenial("password authentication is not supported; use public key authentication"),
	), nil
}

// OnPubKey performs one exact authentik lookup for the public-key string
// supplied by ContainerSSH.
func (h *authHandler) OnPubKey(
	meta metadata.ConnectionAuthPendingMetadata,
	publicKey auth.PublicKey,
) (authDecision, error) {
	start := time.Now()
	user, err := h.authentik.lookupUser(context.Background(), publicKey.PublicKey)
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
			Error(message.NewMessage(
				logCodePubKeyError,
				"Public key authentication failed for %s: authentik lookup error: %v",
				meta.Username, err,
			))
		return authDecision{meta: meta.AuthFailed()}, err
	}
	if user == nil {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: key is not uniquely enrolled in authentik",
				meta.Username,
			))
		return deny(
			meta.AuthFailed(),
			userDenial("public key is not enrolled for any user"),
		), nil
	}
	if !user.IsActive {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("owner", message.LabelValue(user.Username)).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: owner account %s is inactive",
				meta.Username, user.Username,
			))
		return deny(
			meta.AuthFailed(),
			userDenial("the account owning this key is inactive"),
		), nil
	}

	h.logger.WithLabel("username", message.LabelValue(meta.Username)).
		WithLabel("owner", message.LabelValue(user.Username)).
		WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
		Info(message.NewMessage(
			logCodePubKeySuccess,
			"Public key authentication succeeded for %s (owner: %s)",
			meta.Username, user.Username,
		))
	// The SSH username selects the pod template; authenticated metadata records
	// the verified authentik owner.
	return allow(meta.Authenticated(user.Username)), nil
}

// OnAuthorization runs after a successful authentication. Template denials are
// ordinary authz failures, not webhook errors: ContainerSSH rejects immediately
// instead of retrying config requests until its timeout. The denial carries a
// user-facing message the client sees as an authentication banner. The optional
// group gate still applies to allowed templates.
func (h *authHandler) OnAuthorization(
	meta metadata.ConnectionAuthenticatedMetadata,
) (authDecision, error) {
	if h.cfg.AllowedTemplates != nil && !slices.Contains(h.cfg.AllowedTemplates, string(meta.Username)) {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			Info(message.NewMessage(
				logCodeAuthzDenied,
				"Authorization denied: no pod template matches SSH username %s",
				meta.Username,
			))
		return deny(meta, userDenial("no pod template matches SSH username '%s'", meta.Username)), nil
	}
	if h.cfg.RequireGroup == "" {
		return allow(meta), nil
	}
	member, err := h.authentik.userInGroup(context.Background(), meta.AuthenticatedUsername, h.cfg.RequireGroup)
	if err != nil {
		h.logger.WithLabel("username", message.LabelValue(meta.AuthenticatedUsername)).
			Error(message.NewMessage(
				logCodeAuthzError,
				"Authorization failed for %s: group lookup error: %v",
				meta.AuthenticatedUsername, err,
			))
		return authDecision{meta: meta.AuthFailed()}, err
	}
	if !member {
		h.logger.WithLabel("username", message.LabelValue(meta.AuthenticatedUsername)).
			WithLabel("group", message.LabelValue(h.cfg.RequireGroup)).
			Debug(message.NewMessage(
				logCodeAuthzDenied,
				"Authorization denied for %s: not a member of group %s",
				meta.AuthenticatedUsername, h.cfg.RequireGroup,
			))
		return deny(
			meta,
			userDenial("user '%s' is not a member of the required group '%s'",
				meta.AuthenticatedUsername, h.cfg.RequireGroup),
		), nil
	}
	return allow(meta), nil
}
