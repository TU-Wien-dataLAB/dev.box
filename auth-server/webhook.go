// webhook.go implements the ContainerSSH authentication webhook HTTP protocol
// with user-facing denial messages (fork PR #10).
//
// The stock go.containerssh.io/containerssh v0.6.0 auth/webhook server package
// cannot attach messages: it serializes only the success flag and maps handler
// errors to HTTP 500. This local handler preserves that wire contract and adds
// the optional "message" field on policy denials:
//
//   - HTTP 200 + success=false + message: policy denial; the fork gateway makes
//     it a terminal failure and delivers the message to the SSH client as a
//     user authentication banner (RFC 4252, section 5.4) right before the
//     authentication failure.
//   - HTTP 200 + success=false, no message: policy denial without a message;
//     behaves exactly like stock v0.6.0.
//   - HTTP 500: infrastructure failure; ContainerSSH retries these until its
//     auth timeout. Never carries a user-facing message (fail closed).
//   - Unknown path: 404.
//
// A message-less response is JSON-compatible with stock v0.6.0, so this server
// also works against an unpatched gateway (the message field is ignored).
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.containerssh.io/containerssh/auth"
	"go.containerssh.io/containerssh/log"
)

// authResponseBody mirrors auth.ResponseBody plus the fork's optional
// user-facing denial message. The embedded struct marshals inline, so a
// message-less response is byte-compatible with stock v0.6.0.
type authResponseBody struct {
	auth.ResponseBody
	Message string `json:"message,omitempty"`
}

// newWebhookHandler returns the HTTP handler serving the /password, /pubkey,
// and /authz auth webhook endpoints for handler.
func newWebhookHandler(handler *authHandler, logger log.Logger) http.Handler {
	return &webhookHandler{handler: handler, logger: logger}
}

type webhookHandler struct {
	handler *authHandler
	logger  log.Logger
}

func (h *webhookHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch strings.Split(request.URL.Path, "/")[len(strings.Split(request.URL.Path, "/"))-1] {
	case "password":
		h.servePassword(writer, request)
	case "pubkey":
		h.servePubKey(writer, request)
	case "authz":
		h.serveAuthz(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (h *webhookHandler) servePassword(writer http.ResponseWriter, request *http.Request) {
	var requestObject auth.PasswordAuthRequest
	if err := decodeRequest(request, &requestObject); err != nil {
		h.writeDecodeError(writer, "password", err)
		return
	}
	password, err := base64.StdEncoding.DecodeString(requestObject.Password)
	if err != nil {
		h.writeDecodeError(writer, "password", fmt.Errorf("failed to decode password (%w)", err))
		return
	}
	decision, err := h.handler.OnPassword(requestObject.ConnectionAuthPendingMetadata, password)
	h.writeDecision(writer, decision, err)
}

func (h *webhookHandler) servePubKey(writer http.ResponseWriter, request *http.Request) {
	var requestObject auth.PublicKeyAuthRequest
	if err := decodeRequest(request, &requestObject); err != nil {
		h.writeDecodeError(writer, "pubkey", err)
		return
	}
	decision, err := h.handler.OnPubKey(requestObject.ConnectionAuthPendingMetadata, requestObject.PublicKey)
	h.writeDecision(writer, decision, err)
}

func (h *webhookHandler) serveAuthz(writer http.ResponseWriter, request *http.Request) {
	var requestObject auth.AuthorizationRequest
	if err := decodeRequest(request, &requestObject); err != nil {
		h.writeDecodeError(writer, "authz", err)
		return
	}
	decision, err := h.handler.OnAuthorization(requestObject.ConnectionAuthenticatedMetadata)
	h.writeDecision(writer, decision, err)
}

func decodeRequest(request *http.Request, target interface{}) error {
	return json.NewDecoder(request.Body).Decode(target)
}

func (h *webhookHandler) writeDecodeError(writer http.ResponseWriter, endpoint string, err error) {
	h.logger.Debug(fmt.Sprintf("failed to decode %s request: %v", endpoint, err))
	writeResponse(writer, http.StatusInternalServerError, authResponseBody{})
}

// writeDecision maps a handler decision onto the webhook wire contract: an
// error stays a fail-closed HTTP 500 without a message; a policy denial is
// HTTP 200 with the optional user-facing message.
func (h *webhookHandler) writeDecision(writer http.ResponseWriter, decision authDecision, err error) {
	if err != nil {
		writeResponse(writer, http.StatusInternalServerError, authResponseBody{
			ResponseBody: auth.ResponseBody{
				ConnectionAuthenticatedMetadata: decision.meta,
				Success:                         false,
			},
		})
		return
	}
	writeResponse(writer, http.StatusOK, authResponseBody{
		ResponseBody: auth.ResponseBody{
			ConnectionAuthenticatedMetadata: decision.meta,
			Success:                         decision.success,
		},
		Message: decision.message,
	})
}

func writeResponse(writer http.ResponseWriter, status int, body authResponseBody) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		// The status code is already sent; nothing left but to log.
		_ = err
	}
}
