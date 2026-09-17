package main

import (
	"context"
	"fmt"
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

// authConfig carries the optional post-auth group policy.
type authConfig struct {
	// RequireGroup, when set, makes OnAuthorization demand membership of this
	// authentik group before the session starts.
	RequireGroup string
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
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
	h.logger.WithLabel("username", message.LabelValue(meta.Username)).
		Debug(message.NewMessage(
			logCodePassDenied,
			"Password authentication denied for user %s",
			meta.Username,
		))
	return false, meta.AuthFailed(), nil
}

// OnPubKey performs one exact authentik lookup for the public-key string
// supplied by ContainerSSH.
func (h *authHandler) OnPubKey(
	meta metadata.ConnectionAuthPendingMetadata,
	publicKey auth.PublicKey,
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
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
		return false, meta.AuthFailed(), err
	}
	if user == nil {
		h.logger.WithLabel("username", message.LabelValue(meta.Username)).
			WithLabel("durationMs", message.LabelValue(fmt.Sprint(durationMs))).
			Debug(message.NewMessage(
				logCodePubKeyDenied,
				"Public key authentication denied for %s: key is not uniquely enrolled in authentik",
				meta.Username,
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
	return true, meta.Authenticated(user.Username), nil
}

// OnAuthorization runs after a successful authentication. By default it allows
// everyone through; with AUTH_SERVER_REQUIRE_GROUP set it gates access on
// membership of that authentik group.
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
