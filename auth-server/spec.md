# Authentication webhook wire contract

This reference covers the local HTTP adapter in `webhook.go`. The [README](README.md) owns
key enrollment, exact authentik lookup, environment configuration, deployment and security behavior.

## ContainerSSH webhook contract

The adapter uses ContainerSSH's request/response types and adds an optional `message` field to
policy denials. The stock v0.6.0 `auth/webhook` handler cannot serialize that field. Internal
callbacks return an `authDecision` (success, authenticated metadata, denial message) plus an error,
rather than implementing the stock SDK handler interface.

| Decision | HTTP response |
| --- | --- |
| Active, uniquely enrolled owner | 200, `success: true`, authenticated as owner, no `message` |
| Policy denial | 200, `success: false`, user-facing `message` ending in a newline |
| Authentik/network failure | 500, `success: false`, no `message` |
| Malformed request | 500, `success: false`, no `message` |
| Unknown endpoint | 404 |

Policy messages cover unsupported passwords, non-unique/absent key enrollment, inactive accounts,
unknown templates, and missing group membership. They never contain key material, passwords,
tokens, or infrastructure errors. Errors must not be converted into ordinary policy denials.

## Gateway compatibility

Message-bearing denials require the fork's
[PR #10](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/10), which delivers the message as
an SSH userauth banner before a terminal authentication failure. The chart pins a message-capable
image in `values.yaml`; this is not evidence of a live rollout.

Stock v0.6.0 and unpatched images strictly reject unknown JSON fields: they do not ignore
`message`, and retry instead of promptly denying. Overridden gateway images must include the
patch. Successes and message-less responses retain the stock wire format.

## Verification

Adjacent `webhook_test.go` and `template_authorization_test.go` cover adapter responses and
policy decisions. The [test reference](../tests/README.md) lists the separate Docker checks:
the real auth image against fake authentik, and the real gateway against a stub webhook protect
different boundaries. Neither is live acceptance.
