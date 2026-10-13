// Test-only adapter to ContainerSSH's actual defaulting and webhook merger.
// Usage: go run . BASE_YAML OVERRIDE_YAML OUTPUT_YAML
package main

import (
	"fmt"
	"os"

	"go.containerssh.io/containerssh/config"
	"go.containerssh.io/containerssh/internal/structutils"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: config-merge BASE_YAML OVERRIDE_YAML OUTPUT_YAML")
	}
	var base, override config.AppConfig
	base.Default() // The gateway defaults the base, never the partial response.
	for i, cfg := range []*config.AppConfig{&base, &override} {
		data, err := os.ReadFile(args[i])
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return err
		}
	}
	if err := structutils.Merge(&base, override); err != nil {
		return err
	}
	// --dump-config parses/defaults but exits before factory validation. Call
	// the actual v0.6 policy/backend validators here, without needing kube tokens.
	if err := base.Security.Validate(); err != nil {
		return err
	}
	if err := base.Kubernetes.Pod.Validate(); err != nil {
		return err
	}
	// Match the gateway YAML saver; JSON encodes time.Duration numerically,
	// which the file loader (strict YAML) cannot reload.
	data, err := yaml.Marshal(base)
	if err != nil {
		return err
	}
	return os.WriteFile(args[2], data, 0o600)
}
