package main

// Trusted inputs and deterministic box identity, shared with the config
// server's persistent naming. Nothing in this file reads client-controlled
// data: the owner and template list arrive only through the operator-owned
// webhook response environment.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// ownerEnv carries the server-verified authenticated identity.
	ownerEnv = "DEV_BOX_AUTHENTICATED_OWNER"
	// templatesEnv carries the owner's current ordinary persistent box
	// template names, comma-separated, from the operator-managed store.
	templatesEnv = "DEV_BOX_BOX_TEMPLATES"
)

// boxPodName derives the deterministic persistent pod name for an (owner,
// template) pair: "box-" plus the first 10 hexadecimal SHA-256 characters over
// the trimmed owner, a NUL separator, and the template name. It must match
// the config server's persistent naming exactly (the owner Role grants
// get/delete on precisely these names).
func boxPodName(owner, template string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + template))
	return "box-" + hex.EncodeToString(sum[:])[:10]
}

// parseTemplates splits the trusted template list. An unset or empty value is
// a valid empty list (the owner has no persistent box templates); names are
// validated so only operator-published template names can ever reach a row,
// and duplicates collapse. Any malformed entry fails closed.
func parseTemplates(value string) ([]string, error) {
	var templates []string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !validTemplateName(name) {
			return nil, fmt.Errorf("trusted %s contains invalid template name %q; contact the operator", templatesEnv, name)
		}
		if !seen[name] {
			seen[name] = true
			templates = append(templates, name)
		}
	}
	sort.Strings(templates)
	return templates, nil
}

// validTemplateName matches the config server's template-name charset so the
// two sides can never disagree about which names exist.
func validTemplateName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// formatUptime renders the time since a pod started, e.g. "up 3d4h".
func formatUptime(started time.Time, now time.Time) string {
	if started.IsZero() {
		return "—"
	}
	d := now.Sub(started)
	if d < time.Minute {
		return "up <1m"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("up %dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("up %dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("up %dm", minutes)
	}
}

// formatLimits renders a container's resource limits as "mem/cpu", e.g.
// "1Gi/500m". Kubernetes quantities are printed as declared.
func formatLimits(limits map[string]string) string {
	memory, cpu := limits["memory"], limits["cpu"]
	switch {
	case memory != "" && cpu != "":
		return memory + "/" + cpu
	case memory != "":
		return memory
	case cpu != "":
		return cpu
	default:
		return "—"
	}
}
