package main

import (
	"fmt"
	"strings"

	"go.containerssh.io/containerssh/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	settingsTemplateName = "settings"
	settingsOwnerEnv     = "DEV_BOX_AUTHENTICATED_OWNER"
	settingsExecutable   = "/usr/local/bin/dev-box-settings"
)

// settingsConfig is the reserved application, not a user-editable template.
// Every response owns its maps/slices; neither template files nor client input
// can modify its policy, image, or verified owner.
func (h *configHandler) settingsConfig(req config.Request) (config.AppConfig, error) {
	owner := req.AuthenticatedUsername
	if strings.TrimSpace(owner) == "" {
		return config.AppConfig{}, errEmptyAuthenticatedIdentity
	}
	if h.settingsImage == "" {
		return config.AppConfig{}, fmt.Errorf("settings image is not configured")
	}
	account, err := h.ensureOwnerAccess(owner)
	if err != nil {
		return config.AppConfig{}, fmt.Errorf("ensure settings owner access: %w", err)
	}
	return config.AppConfig{
		Security: config.SecurityConfig{
			DefaultMode:  config.ExecutionPolicyDisable,
			ForceCommand: settingsExecutable,
			MaxSessions:  1,
			Shell:        config.SecurityShellConfig{Mode: config.ExecutionPolicyEnable},
			TTY:          config.SecurityTTYConfig{Mode: config.ExecutionPolicyEnable},
			Command:      config.CommandConfig{Mode: config.ExecutionPolicyDisable},
			Subsystem:    config.SubsystemConfig{Mode: config.ExecutionPolicyDisable},
			Env:          config.SecurityEnvConfig{Mode: config.ExecutionPolicyDisable},
			Signal:       config.SecuritySignalConfig{Mode: config.ExecutionPolicyDisable},
			Forwarding: config.ForwardingConfig{
				ForwardingMode:        config.ExecutionPolicyDisable,
				ReverseForwardingMode: config.ExecutionPolicyDisable,
				SocketForwardingMode:  config.ExecutionPolicyDisable,
				SocketListenMode:      config.ExecutionPolicyDisable,
				X11ForwardingMode:     config.ExecutionPolicyDisable,
			},
		},
		Kubernetes: config.KubernetesConfig{Pod: config.KubernetesPodConfig{
			Mode:         config.KubernetesExecutionModeConnection,
			Metadata:     metav1.ObjectMeta{GenerateName: "settings-", Labels: map[string]string{"dev.box/application": "settings"}},
			ShellCommand: []string{settingsExecutable},
			AgentPath:    "/usr/bin/containerssh-agent",
			IdleCommand:  []string{"/usr/bin/containerssh-agent", "wait-signal", "--signal", "INT", "--signal", "TERM"},
			Spec: corev1.PodSpec{
				ServiceAccountName:            account,
				AutomountServiceAccountToken:  ptr.To(true),
				RestartPolicy:                 corev1.RestartPolicyNever,
				TerminationGracePeriodSeconds: ptr.To(int64(5)),
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				Containers: []corev1.Container{{
					Name: "settings", Image: h.settingsImage,
					Env: []corev1.EnvVar{{Name: settingsOwnerEnv, Value: owner}},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
						Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
					},
				}},
			},
		}},
	}, nil
}
