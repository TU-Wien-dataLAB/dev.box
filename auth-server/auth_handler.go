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
)

// authHandler implements ContainerSSH's required authentication interface.
// Only public-key authentication can succeed.
type authHandler struct {
	authentik *authentikClient
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

// OnAuthorization exists because ContainerSSH's handler interface requires it.
// Authentication policy is fully decided by OnPubKey.
func (h *authHandler) OnAuthorization(
	meta metadata.ConnectionAuthenticatedMetadata,
) (bool, metadata.ConnectionAuthenticatedMetadata, error) {
	return true, meta, nil
}
