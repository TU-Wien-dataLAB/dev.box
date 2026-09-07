package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
)

// Log message codes for the normalizing sync.
const (
	logCodeSyncStart    = "AUTH_SERVER_SYNC_START"
	logCodeSyncFinish   = "AUTH_SERVER_SYNC_FINISH"
	logCodeSyncUpdate   = "AUTH_SERVER_SYNC_UPDATE"
	logCodeSyncProblem  = "AUTH_SERVER_SYNC_PROBLEM"
	logCodeSyncAPIError = "AUTH_SERVER_SYNC_API_ERROR"
)

// syncRunner periodically normalizes user SSH keys in authentik (spec §5.3,
// option (a)):
//
//   - for every user with attributes.ssh_public_key set, parse + canonicalize
//     the armored key (dropping comments/whitespace) and (re)compute the
//     "SHA256:..." fingerprint;
//   - persist both canonical key and fingerprint back to the user's attributes
//     (so the hot-path OnPubKey lookup keeps working against an exact scalar
//     attribute match);
//   - detect the same fingerprint being bound to more than one user -> logged
//     as an integrity violation and neither user is re-written.
//
// It never deletes user attributes; with write access disabled it runs as a
// dry-run and only reports what it would change.
type syncRunner struct {
	authentik    *authentikClient
	interval     time.Duration
	writeEnabled bool
	logger       log.Logger
}

// syncUsers runs one full pass over all authentik users and returns
// (updated, usersScanned, problems, error).
func (s *syncRunner) syncUsers(ctx context.Context) (int, int, []string, error) {
	users, err := s.authentik.listAllUsers(ctx)
	if err != nil {
		return 0, 0, nil, err
	}

	seenFingerprint := map[string]string{} // fingerprint -> username
	updated := 0
	var problems []string

	for i := range users {
		user := &users[i]
		raw, _ := user.Attributes[attrSSHPublicKey].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		fingerprint, canonical, err := fingerprintAndCanonicalKey(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"user %s: attributes.%s (%q) is not a valid public key: %v",
				user.Username, attrSSHPublicKey, raw, err,
			))
			continue
		}
		if previous, duplicate := seenFingerprint[fingerprint]; duplicate {
			problems = append(problems, fmt.Sprintf(
				"key uniqueness violated: fingerprint %s is shared by users %s and %s; neither is re-written",
				fingerprint, previous, user.Username,
			))
			continue
		}
		seenFingerprint[fingerprint] = user.Username

		// Attributes may be absent entirely; build a clean copy to patch.
		attrs := make(map[string]interface{}, len(user.Attributes)+2)
		for k, v := range user.Attributes {
			attrs[k] = v
		}
		storedKey, _ := user.Attributes[attrSSHPublicKey].(string)
		storedFingerprint, _ := user.Attributes[attrSSHKeyFingerprint].(string)
		if strings.TrimSpace(storedKey) == canonical && storedFingerprint == fingerprint {
			continue // already normalized
		}
		attrs[attrSSHPublicKey] = canonical
		attrs[attrSSHKeyFingerprint] = fingerprint
		if !s.writeEnabled {
			s.logger.WithLabel("username", message.LabelValue(user.Username)).
				WithLabel("fingerprint", message.LabelValue(fingerprint)).
				Debug(message.NewMessage(
					logCodeSyncStart,
					"Sync (dry-run): would normalize key of %s to fingerprint %s",
					user.Username, fingerprint,
				))
			continue
		}
		if err := s.authentik.updateUserAttributes(ctx, user.PK, attrs); err != nil {
			problems = append(problems, fmt.Sprintf(
				"user %s: failed to write normalized attributes: %v", user.Username, err,
			))
			continue
		}
		updated++
		s.logger.WithLabel("username", message.LabelValue(user.Username)).
			WithLabel("fingerprint", message.LabelValue(fingerprint)).
			Info(message.NewMessage(
				logCodeSyncUpdate,
				"Sync: normalized SSH public key of %s (fingerprint %s)",
				user.Username, fingerprint,
			))
	}
	return updated, len(users), problems, nil
}

// run loops the sync on interval until ctx is cancelled. A fatal API failure
// aborts the loop (fail-closed for the sync; the auth hot path is unaffected).
func (s *syncRunner) run(ctx context.Context) {
	s.logger.Info(message.NewMessage(
		logCodeSyncStart,
		"SSH key normalizing sync started (interval %s, write access %v)",
		s.interval, s.writeEnabled,
	))
	for {
		updated, total, problems, err := s.syncUsers(ctx)
		if err != nil {
			s.logger.Error(message.NewMessage(
				logCodeSyncAPIError,
				"Sync pass failed: %v", err,
			))
			return
		}
		for _, p := range problems {
			s.logger.Error(message.NewMessage(
				logCodeSyncProblem,
				"Sync problem: %s", p,
			))
		}
		s.logger.Info(message.NewMessage(
			logCodeSyncFinish,
			"Sync pass finished: %d users scanned, %d updated, %d problems",
			total, updated, len(problems),
		))

		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval):
		}
	}
}
