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
// # Persistent mode
//
// When CONTAINERSSH_OPERATING_MODE=persistent, the server derives a
// deterministic, collision-resistant DNS-1123 pod name from the canonical
// authenticated identity (authenticatedUsername — never the client-chosen SSH
// username) plus the resolved template name, injects it as
// kubernetes.pod.metadata.name, and labels the pod with dev.box/owner. It also
// caps the number of live boxes per owner (CONTAINERSSH_MAX_PODS_PER_USER,
// default 3), denying new boxes at the cap while always allowing reconnects to
// an existing box. An empty authenticated identity and any pod-listing failure
// are denied (fail closed). Injection is skipped for a template that explicitly
// selects a non-persistent execution mode.
//
// Environment:
//
//	CONTAINERSSH_CONFIG_DIR         directory with pod template files (default /config)
//	CONTAINERSSH_LISTEN             listen address (default 0.0.0.0:8080)
//	CONTAINERSSH_LOG_LEVEL          0 emergency .. 7 debug (syslog-style numbering, default 6 = info)
//	CONTAINERSSH_OPERATING_MODE     connection, session or persistent (default connection).
//	                               Only persistent enables name/label injection and the cap.
//	CONTAINERSSH_SESSION_NAMESPACE  namespace of the backend user pods (default containerssh-sessions)
//	CONTAINERSSH_MAX_PODS_PER_USER  per-owner live-box cap; <= 0 disables the cap (default 3)
//	CONTAINERSSH_TLS_CERT           server certificate (file path or PEM)
//	CONTAINERSSH_TLS_KEY            server private key (file path or PEM)
//	CONTAINERSSH_TLS_CLIENTCA       optional CA to verify clients (mTLS) - file path or PEM
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/config/webhook"
	"go.containerssh.io/containerssh/log"
	"go.containerssh.io/containerssh/message"
	"go.containerssh.io/containerssh/service"
)

func main() {
	dir := env("CONTAINERSSH_CONFIG_DIR", "/config")
	listen := env("CONTAINERSSH_LISTEN", "0.0.0.0:8080")

	logger, err := log.NewLogger(config.LogConfig{
		Level:       config.LogLevel(envInt("CONTAINERSSH_LOG_LEVEL", 6)),
		Format:      config.LogFormatLJSON,
		Destination: config.LogDestinationStdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// Persistent-mode settings, signalled through the environment so the same
	// image serves bundled (chart-rendered) and external operators.
	operatingMode := config.KubernetesExecutionMode(env("CONTAINERSSH_OPERATING_MODE", "connection"))
	if err := operatingMode.Validate(); err != nil {
		logger.Critical(message.NewMessage(
			"CONFIG_SERVER_INVALID_OPERATING_MODE",
			"Invalid CONTAINERSSH_OPERATING_MODE %q: %v", operatingMode, err,
		))
		os.Exit(1)
	}
	handler := &configHandler{
		dir:    dir,
		logger: logger,
		cache:  map[string]cachedEntry{},
		boxes: persistentBoxConfig{
			operatingMode: operatingMode,
			namespace:     env("CONTAINERSSH_SESSION_NAMESPACE", "containerssh-sessions"),
			maxPods:       envInt("CONTAINERSSH_MAX_PODS_PER_USER", 3),
		},
	}
	if handler.boxes.enabled() {
		handler.boxes.pods, err = newKubePodLister()
		if err != nil {
			logger.Critical(message.NewMessage(
				"CONFIG_SERVER_PERSISTENT_SETUP_FAILED",
				"Failed to initialize persistent-mode pod listing: %v", err,
			))
			os.Exit(1)
		}
		if handler.boxes.maxPods <= 0 {
			logger.Warning(message.NewMessage(
				"CONFIG_SERVER_CAP_DISABLED",
				"CONTAINERSSH_MAX_PODS_PER_USER is %d; the per-user box cap is disabled",
				handler.boxes.maxPods,
			))
		}
	}

	// HTTP/TLS configuration. Setting CONTAINERSSH_TLS_CERT + _KEY enables HTTPS;
	// adding CONTAINERSSH_TLS_CLIENTCA additionally requires client certificates (mTLS).
	httpConfig := config.HTTPServerConfiguration{Listen: listen}
	if cert, key := env("CONTAINERSSH_TLS_CERT", ""), env("CONTAINERSSH_TLS_KEY", ""); cert != "" && key != "" {
		httpConfig.Cert = cert
		httpConfig.Key = key
		httpConfig.ClientCACert = env("CONTAINERSSH_TLS_CLIENTCA", "")
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

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
