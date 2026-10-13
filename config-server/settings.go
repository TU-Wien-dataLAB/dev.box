package main

import (
	"fmt"
	"strings"

	"go.containerssh.io/containerssh/config"
	corev1 "k8s.io/api/core/v1"
)

const settingsOwnerEnv = "DEV_BOX_AUTHENTICATED_OWNER"

// settings is a reserved application template, never a persistent user box.
// Only the authenticated identity from the gateway may populate its owner env.
func applySettings(req config.Request, cfg config.AppConfig) (config.AppConfig, error) {
	owner := req.AuthenticatedUsername
	if strings.TrimSpace(owner) == "" {
		return config.AppConfig{}, errEmptyAuthenticatedIdentity
	}
	pod := &cfg.Kubernetes.Pod
	if pod.Mode != config.KubernetesExecutionModeConnection || len(pod.Spec.Containers) != 1 || pod.Metadata.Name != "" || pod.CreateMissingPods || pod.Metadata.Labels[ownerLabelKey] != "" {
		return config.AppConfig{}, fmt.Errorf("settings requires one disposable connection-mode application container without persistent box identity")
	}

	// The parsed template is cached and shared. Deep-copy before touching nested
	// slices/maps so simultaneous owners and retained responses remain isolated.
	pod.Spec = *pod.Spec.DeepCopy()
	container := &pod.Spec.Containers[0]
	env := make([]corev1.EnvVar, 0, len(container.Env)+1)
	for _, value := range container.Env {
		if value.Name != settingsOwnerEnv {
			env = append(env, value)
		}
	}
	container.Env = append(env, corev1.EnvVar{Name: settingsOwnerEnv, Value: owner})
	return cfg, nil
}
