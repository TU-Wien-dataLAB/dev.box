// Command config-server is a tiny ContainerSSH configuration server.
//
// It implements the ContainerSSH configuration webhook protocol
// (https://github.com/ContainerSSH/ContainerSSH#building-a-configuration-webhook-server)
// by building on the official go.containerssh.io/containerssh module
// (config/webhook package).
//
// It serves POD TEMPLATES selected by the SSH username: when a user connects
// (e.g. "ssh ubuntu@dev.box.example.com"), the server resolves the template
// that matches the username:
//
//	/config/<username>.yaml   pod template named after the username
//	/config/default.yaml      catch-all for users without their own template
//	(empty)                   otherwise ContainerSSH uses its base config
//
// Each template is a partial AppConfig:
//
//	# /config/ubuntu.yaml
//	kubernetes:
//	  pod:
//	    metadata:
//	      labels:
//	        template: ubuntu
//	    spec:
//	      containers:
//	        - name: shell
//	          image: ubuntu:22.04
//	          command: ["/bin/bash"]
//
// The directory is typically fed by a mounted Kubernetes ConfigMap. Unset
// fields are inherited from ContainerSSH's base config (the chart's generated
// config.yaml). If no template matches, an empty config is returned so
// ContainerSSH uses the base config unchanged.
//
// Environment:
//
//	CONTAINERSSH_CONFIG_DIR   directory with pod template files (default /config)
//	CONTAINERSSH_LISTEN       listen address (default 0.0.0.0:8080)
//	CONTAINERSSH_LOG_LEVEL    0 emergency .. 7 debug (syslog-style numbering, default 6 = info)
//	CONTAINERSSH_TLS_CERT     server certificate (file path or PEM)
//	CONTAINERSSH_TLS_KEY      server private key (file path or PEM)
//	CONTAINERSSH_TLS_CLIENTCA optional CA to verify clients (mTLS) - file path or PEM
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/config/webhook"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
	"go.containerssh.io/containerssh/service"

	"gopkg.in/yaml.v3"
)

const (
	// logCodeTemplateServed is logged when a pod template is served.
	logCodeTemplateServed = "CONFIG_SERVER_TEMPLATE_SERVED"
	// logCodeNoTemplates is logged when no template matches the username.
	logCodeNoTemplates = "CONFIG_SERVER_NO_TEMPLATES"
	// logCodeParseError is logged when a template file is malformed.
	logCodeParseError = "CONFIG_SERVER_TEMPLATE_PARSE_ERROR"
)

// defaultTemplateName is the catch-all template file (without extension) used
// when the user has no template named after their username.
const defaultTemplateName = "default"

// cachedEntry remembers a parsed template together with the file stat it was
// parsed from, so unchanged files are not re-read on every SSH connection.
type cachedEntry struct {
	cfg     config.AppConfig
	size    int64
	modTime time.Time
}

// configHandler implements config.RequestHandler. Each user's request is served
// the pod template named after their username (or the default template).
type configHandler struct {
	dir    string
	logger log.Logger
	mu     sync.Mutex
	cache  map[string]cachedEntry
}

// OnConfig is called by ContainerSSH on every authenticated SSH connection.
func (h *configHandler) OnConfig(req config.Request) (config.AppConfig, error) {
	cfg, source, err := h.load(req.Username)
	if err != nil {
		h.logger.WithLabel("username", message.LabelValue(req.Username)).
			Error(message.NewMessage(
				logCodeParseError,
				"Failed to serve a pod template for user %s: %v",
				req.Username, err,
			))
		return config.AppConfig{}, err
	}
	if source == "" {
		// No matching template: inherit the base config untouched.
		h.logger.WithLabel("username", message.LabelValue(req.Username)).
			Debug(message.NewMessage(
				logCodeNoTemplates,
				"No pod template matches user %s, using base configuration",
				req.Username,
			))
		return config.AppConfig{}, nil
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

// load returns the template for username, the file it came from ("" = none),
// or an error if a matching file exists but cannot be parsed.
func (h *configHandler) load(username string) (config.AppConfig, string, error) {
	for _, name := range h.candidates(username) {
		path, err := safeJoin(h.dir, name)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			// Template not present: try the next candidate.
			continue
		}
		cfg, err := h.readCached(path, info)
		if err != nil {
			return config.AppConfig{}, path, err
		}
		return cfg, path, nil
	}
	return config.AppConfig{}, "", nil
}

// candidates lists the template files to try for a username, always ending
// with the default catch-all template.
func (h *configHandler) candidates(username string) []string {
	u := sanitize(username)
	if u != defaultTemplateName {
		return []string{u + ".yaml", u + ".json", defaultTemplateName + ".yaml", defaultTemplateName + ".json"}
	}
	return []string{defaultTemplateName + ".yaml", defaultTemplateName + ".json"}
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

// sanitize makes a username safe to use as a file name.
func sanitize(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return defaultTemplateName
	}
	var b strings.Builder
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
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

func main() {
	dir := env("CONTAINERSSH_CONFIG_DIR", "/config")
	listen := env("CONTAINERSSH_LISTEN", "0.0.0.0:8080")

	logger, err := log.NewLogger(config.LogConfig{
		Level:       config.LogLevel(parseInt(env("CONTAINERSSH_LOG_LEVEL", "6"))),
		Format:      config.LogFormatLJSON,
		Destination: config.LogDestinationStdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// HTTP/TLS configuration. Setting CONTAINERSSH_TLS_CERT + _KEY enables HTTPS;
	// adding CONTAINERSSH_TLS_CLIENTCA additionally requires client certificates (mTLS).
	httpConfig := config.HTTPServerConfiguration{Listen: listen}
	if cert, key := env("CONTAINERSSH_TLS_CERT", ""), env("CONTAINERSSH_TLS_KEY", ""); cert != "" && key != "" {
		httpConfig.Cert = cert
		httpConfig.Key = key
		httpConfig.ClientCACert = env("CONTAINERSSH_TLS_CLIENTCA", "")
	}

	handler := &configHandler{
		dir:    dir,
		logger: logger,
		cache:  map[string]cachedEntry{},
	}

	srv, err := webhook.NewServer(httpConfig, handler, logger)
	if err != nil {
		logger.Critical(message.NewMessage(
			"CONFIG_SERVER_START_FAILED",
			"Failed to start the configuration server: %v", err,
		))
		os.Exit(1)
	}
	lifecycle := service.NewLifecycle(srv)

	go func() {
		if err := lifecycle.Run(); err != nil {
			logger.Critical(message.NewMessage(
				"CONFIG_SERVER_RUN_FAILED",
				"The configuration server terminated with an error: %v", err,
			))
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if _, ok := <-signals; ok {
			logger.Info(message.NewMessage(
				"CONFIG_SERVER_SHUTDOWN",
				"Shutting down the configuration server...",
			))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			lifecycle.Stop(ctx)
		}
	}()

	lastError := lifecycle.Wait()
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	close(signals)

	if lastError != nil {
		logger.Critical(message.NewMessage(
			"CONFIG_SERVER_EXIT_ERROR",
			"An error happened while running the configuration server (%v)",
			lastError,
		))
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func parseInt(s string) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		// default to info
		return 6
	}
	return n
}
