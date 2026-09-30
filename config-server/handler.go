package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"

	"gopkg.in/yaml.v3"
)

const (
	// logCodeTemplateServed is logged when a pod template is served.
	logCodeTemplateServed = "CONFIG_SERVER_TEMPLATE_SERVED"
	// logCodeNoTemplates is logged when no template matches the username.
	logCodeNoTemplates = "CONFIG_SERVER_NO_TEMPLATES"
	// logCodeParseError is logged when a template file is malformed.
	logCodeParseError = "CONFIG_SERVER_TEMPLATE_PARSE_ERROR"
	// logCodePersistentDenied is logged when the persistent-mode gate denies a connection.
	logCodePersistentDenied = "CONFIG_SERVER_PERSISTENT_DENIED"
	// logCodePersistentInjected is logged when a deterministic pod name is injected.
	logCodePersistentInjected = "CONFIG_SERVER_PERSISTENT_INJECTED"
)

var errNoMatchingTemplate = errors.New("no pod template matches SSH username")

// cachedEntry remembers a parsed template together with the file stat it was
// parsed from, so unchanged files are not re-read on every SSH connection.
type cachedEntry struct {
	cfg     config.AppConfig
	size    int64
	modTime time.Time
}

// configHandler implements config.RequestHandler. Each user's request is served
// the pod template named exactly after their username. Unmatched usernames
// are denied; there is no default-template or base-config fallback.
//
// In persistent mode it additionally derives the box identity from the
// authenticated identity (not the client-chosen username), injects a
// deterministic DNS-1123 pod name plus the owner label, and enforces the
// per-user pod cap. See persistent.go for that contract.
type configHandler struct {
	dir    string
	logger log.Logger
	mu     sync.Mutex
	cache  map[string]cachedEntry
	boxes  persistentBoxConfig
}

// OnConfig is called by ContainerSSH on every authenticated SSH connection.
func (h *configHandler) OnConfig(req config.Request) (config.AppConfig, error) {
	cfg, source, err := h.load(req.Username)
	if err != nil {
		code := logCodeParseError
		if errors.Is(err, errNoMatchingTemplate) {
			code = logCodeNoTemplates
		}
		h.logger.WithLabel("username", message.LabelValue(req.Username)).
			Error(message.NewMessage(
				code,
				"Failed to serve a pod template for user %s: %v",
				req.Username, err,
			))
		return config.AppConfig{}, err
	}
	name := filepath.Base(source)
	templateName := strings.TrimSuffix(name, filepath.Ext(name))

	// Persistent-mode injection is skipped for a template that explicitly
	// selects a non-persistent mode (a fixed per-user name would break
	// per-connection/session pod creation); the template's own mode applies.
	if h.boxes.enabled() && !templateSelectsNonPersistent(cfg) {
		cfg, err = h.applyPersistent(req, cfg, templateName)
		if err != nil {
			return config.AppConfig{}, err
		}
	}

	h.logger.WithLabel("username", message.LabelValue(req.Username)).
		WithLabel("template", message.LabelValue(filepath.Base(source))).
		Info(message.NewMessage(
			logCodeTemplateServed,
			"Serving pod template %s for user %s",
			filepath.Base(source), req.Username,
		))
	return cfg, nil
}

// load returns the named template and its source file, or an error when the
// username is invalid, no matching file exists, or the file cannot be read/parsed.
func (h *configHandler) load(username string) (config.AppConfig, string, error) {
	if !validTemplateName(username) {
		return config.AppConfig{}, "", fmt.Errorf("%w %q: invalid template name", errNoMatchingTemplate, username)
	}
	for _, name := range []string{username + ".yaml", username + ".json"} {
		path, err := safeJoin(h.dir, name)
		if err != nil {
			return config.AppConfig{}, "", err
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			// Try the other supported extension, never a different template.
			continue
		}
		if err != nil {
			return config.AppConfig{}, path, err
		}
		cfg, err := h.readCached(path, info)
		if err != nil {
			return config.AppConfig{}, path, err
		}
		return cfg, path, nil
	}
	return config.AppConfig{}, "", fmt.Errorf("%w %q", errNoMatchingTemplate, username)
}

// readCached parses and caches a template file, keyed on its size + mtime.
func (h *configHandler) readCached(path string, info os.FileInfo) (config.AppConfig, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if entry, ok := h.cache[path]; ok &&
		entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		return entry.cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return config.AppConfig{}, err
	}
	var cfg config.AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return config.AppConfig{}, fmt.Errorf("invalid template in %s: %w", path, err)
	}
	if h.cache == nil {
		h.cache = map[string]cachedEntry{}
	}
	h.cache[path] = cachedEntry{cfg: cfg, size: info.Size(), modTime: info.ModTime()}
	return cfg, nil
}

// validTemplateName prevents unsafe paths and lossy aliases to another template.
func validTemplateName(username string) bool {
	if username == "" {
		return false
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// safeJoin returns dir/name after checking that name stays inside dir.
func safeJoin(dir, name string) (string, error) {
	cleanDir := filepath.Clean(dir)
	cleanName := filepath.Clean(name)
	if cleanName == "." || cleanName == "" || strings.Contains(cleanName, "/") {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	path := filepath.Join(cleanDir, cleanName)
	rel, err := filepath.Rel(cleanDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe path %q", path)
	}
	return path, nil
}
