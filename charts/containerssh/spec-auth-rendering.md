# Spec — Chart fails fast without authentication and renders valid auth webhooks

Status: implemented locally; staged cluster validation pending

## Problem Statement

A first real chart installation on the `container-ssh` cluster rendered password authentication as
`method: webhook` with an empty URL. ContainerSSH rejected the configuration with
`CORE_CONFIG_ERROR` (`auth.password.url (invalid URL: )`) and the main Deployment entered
`CrashLoopBackOff`.

Simply omitting the auth block is not a sufficient chart behavior. ContainerSSH v0.6 has a legacy
compatibility nuance: an explicit auth configuration with every authentication method disabled is
rejected, while a completely omitted auth block can let the process listen without a usable
webhook authenticator. Authorization is post-auth and cannot satisfy authentication. The chart
already documents an authentication server as a prerequisite, so it should reject either unusable
input during Helm rendering rather than create a crash-looping or unreachable SSH service.

## Solution

Resolve the effective password, public-key, and authorization webhook URLs before rendering the
ContainerSSH config. Fail Helm rendering with an actionable error unless password or public-key
authentication has an effective URL. An authorization URL alone is insufficient. For valid values,
render only method blocks with usable URLs and preserve the bundled auth-server auto-wiring and
explicit per-method override behavior.

This moves invalid-input failure to the chart boundary and guarantees that every chart-rendered auth
block handed to ContainerSSH has a webhook method paired with a non-empty URL.

## User Stories

1. As a chart operator, I want an installation with no authentication method to fail during Helm
   rendering, so that it never creates a crash-looping ContainerSSH Deployment.
2. As a chart operator, I want the failure message to name the valid configuration choices, so that I
   can enable the bundled auth-server or provide a password/public-key webhook URL.
3. As a chart operator, I want an authorization-only configuration rejected, so that a post-auth
   webhook is not mistaken for an authentication method.
4. As a chart operator, I want password-only external authentication to render a valid password
   webhook without empty public-key or authorization blocks.
5. As a chart operator, I want public-key-only external authentication to render a valid public-key
   webhook without an empty password block.
6. As a chart operator, I want optional authorization rendered only when its URL is configured, so
   that it can augment either supported authentication path.
7. As a chart operator, I want enabling the bundled authentik-backed auth-server to auto-wire
   password, public-key, and authorization webhooks as before, so that the integrated deployment
   remains simple.
8. As a chart operator, I want an explicit per-method URL to retain precedence over the bundled
   auth-server URL for that method, so that mixed bundled/external configurations remain possible.
9. As a chart operator, I want an externally managed ContainerSSH ConfigMap to bypass chart-level
   auth validation, so that the chart does not reject configuration it cannot inspect.
10. As a maintainer, I want valid chart-rendered configs accepted by the real ContainerSSH v0.6
    consumer, so that Helm success does not hide another startup-time config error.
11. As a maintainer, I want the no-auth, authz-only, password-only, public-key-only, bundled, and
    mixed-override cases covered by a repeatable rendering test, so that future template changes
    preserve the contract.
12. As a maintainer, I want the chart documentation and validation commands to state that
    authentication is required, so that defaults are not presented as a runnable deployment.
13. As a maintainer, I want a patch chart-version bump for the behavioral fix, so that packaged chart
    artifacts identify the corrected rendering behavior.

## Implementation Decisions

- **Effective URLs first:** derive one effective URL per auth method. An explicit per-method URL wins;
  otherwise enabling the bundled auth-server supplies its in-cluster Service URL.
- **Authentication invariant:** at least one effective password or public-key URL is required when
  the chart renders the ContainerSSH ConfigMap. Authorization does not count as authentication.
- **Early failure:** if the invariant is not met, Helm rendering fails with an actionable message
  naming the bundled auth-server and the two external webhook values that can satisfy it.
- **Conditional method emission:** render password, public-key, and authorization blocks only when
  their respective effective URL is non-empty. Never render `method: webhook` with an empty URL.
- **Bundled behavior preserved:** enabling the bundled auth-server continues to configure all three
  webhook methods. Its existing authentik URL requirement remains unchanged.
- **External ConfigMap escape hatch:** when an existing ConfigMap supplies the complete ContainerSSH
  config, skip this validation because the chart does not own or inspect that config.
- **No new values:** retain the current values surface and URL precedence. Do not introduce a
  synthetic disabled authenticator or an insecure default webhook.
- **Documentation alignment:** chart prerequisites, values comments, validation commands, and key
  value tables must describe the fail-fast requirement rather than an auth-off runnable mode.
- **Versioning:** increment the chart patch version because rendered behavior changes without
  changing the values schema.

## Testing Decisions

- **What makes a good test:** exercise the chart's public rendering interface and inspect the
  semantic config it emits, not private template variables or whitespace. Valid output is ultimately
  checked by the real ContainerSSH consumer.
- **Invalid-input seam:** chart rendering fails for both no-auth and authorization-only values, with
  the documented actionable error. No Kubernetes resources are applied.
- **Valid-output seam:** password-only, public-key-only, bundled auth-server, and mixed-override
  values render successfully. Each emitted method has the expected non-empty URL, omitted methods
  stay absent, and unrelated config remains semantically unchanged.
- **Consumer compatibility:** representative valid rendered configs are fed to
  `containerssh/containerssh:v0.6 --dump-config` or booted by that exact image during the staged
  cluster validation; acceptance requires exit/startup without `CORE_CONFIG_ERROR`.
- **External-config case:** setting an existing ConfigMap bypasses chart-owned auth validation and
  renders the Deployment successfully.
- **Modules tested:** only the Helm chart's rendering contract. No config-server or auth-server
  application behavior changes are required.
- **Prior art:** the repository runbook identifies real-binary `--dump-config` as the strongest
  config validation; the initial cluster install is the failing reproduction; the chart already
  uses render-time `required` checks for bundled authentik configuration.

## Out of Scope

- Supporting an intentionally unauthenticated or deny-all ContainerSSH listener.
- Treating authorization as authentication.
- Adding keyboard-interactive, Kerberos, or GSSAPI values to the chart.
- Changing authentication or authorization behavior inside either webhook server.
- Changing production authentik enrollment, group policy, or service-token permissions.
- Host-key generation and persistence; its warning was unrelated to the auth config failure.
- Full SSH and backend user-pod acceptance testing, which is specified separately in the staged cluster
  validation plan.
- Implementing the Docker-compose webhook contract framework.
- Adding a GitHub Actions chart validation or release workflow; the rendering matrix remains a local
  validation command for now.

## Further Notes

- The previously drafted omission-only fix is insufficient: due to ContainerSSH v0.6 legacy
  defaults it can reach Ready, but it has no usable chart-configured authenticator.
- The rendering regression matrix now passes locally. The aborted cluster upgrade partially applied
  the omission-only draft and left Helm at `pending-upgrade`; that release is not acceptance-ready
  and remains untouched until the staged cluster prerequisites are supplied.
- The subsequent cluster validation will use the bundled auth-server against production authentik,
  an existing read-token Secret, and a dedicated enrolled test identity.
