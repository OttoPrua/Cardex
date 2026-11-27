# Anthropic-compatible channels (API1)

API1 adds configuration and isolated execution through Claude Code. It does not
perform a model probe, infer account health, or estimate provider costs.
`channel check` does not send a new model request.

## Register without enabling a channel

The CardeX service administrator edits `$CARDEX_HOME/config.json`. Names and
domains below are placeholders:

```json
{
  "engines": {
    "gateway-a": {
      "protocol": "anthropic-compatible",
      "base_url": "https://api.example.test/anthropic",
      "default_model": "gateway-model-1",
      "credential": {"source": "env", "name": "GATEWAY_A_API_TOKEN"}
    }
  }
}
```

Choose exactly one credential reference:

- `env`: the name of an environment variable of the **CardeX service**, not
  its caller's shell. Use a dedicated variable for this channel. Its value is
  checked only at execution time.
- `keychain`: the macOS generic-password service `name`. Add `account` if
  the service matches several accounts. Duplicate matching items are refused.
  The running user must be able to read that item. CardeX does not create or
  update it.
- `file`: a clean absolute `path` to a regular file owned by the service
  user, containing a single bearer token. Keep it outside repositories and
  CardeX's exported data, with no group/other permissions or extra ACL entries.
  Resolved paths cannot be inside a managed repository or worktree, including
  through aliases. Final symlinks and hard-linked files are refused. Windows file references are refused until their ACLs
  can be verified.

Do not put the token in this configuration, a CLI argument or a card. Profiles
do not accept `auth_value`, arbitrary environment additions or shell
interpolation. A profile has one qualified HTTPS origin and optional base path.
Userinfo, query strings, fragments, IP literals, escaped paths and redirects
are not accepted. The provider must accept bearer authentication on the
Anthropic Messages API routes. The runtime permits the SDK's fixed
`?beta=true` Messages qualifier, not a query in the configured URL.

```sh
cardex channel set --id gateway-a --runner claude --engine-profile gateway-a \
    --family gateway-a --model gateway-model-1
cardex channel show gateway-a --json
```

Omit `--model` on creation to use `default_model`. Subsequent updates retain
omitted fields. A compatible binding is persisted as
`anthropic-compatible:gateway-a@<binding-id>` in the existing `engine_profile`
field. The binding ID covers the channel, profile, endpoint, family, models
and credential reference, not the credential value. Editing one of these
requires staging the new identity with `channel set` and approving it in a
separate command. Previously prepared attempts keep their original binding
and refuse to launch if it changes. If its
config is removed or changed to a legacy profile, it cannot fall back to Claude
login, a preset or a legacy credential. Legacy profiles keep their existing
launcher.

Registration neither adds an approved positioning row nor automatically
enables the new channel; it defaults to `degraded`. Only after the operator
has checked the reference and explicitly authorized channel use can a separate
`channel set --id gateway-a --health ok` mark it eligible. Automatic routing
still requires the applicable approved model positioning. A health-only
`channel set --id gateway-a --health degraded` can disable a channel even if
the config or credential cannot be validated; it does not change the binding.
Fixing that config does not automatically reactivate the channel. Do not
enable a real account as part of an API1 configuration rehearsal.

## Execution boundary

CardeX launches a fresh nonpersistent Claude Code session per attempt. It
uses `--bare`, `--restricted` and explicit tool sets, no user or project
settings, and no MCP tools. Only review, research and ranking are enabled,
without tools. Development cards, other card kinds, and browser/goal requests
fail closed; file-tool confinement has not been verified. Use a Claude
Code build implementing these flags; the documented minimum for
`--restricted` is 2.1.248. CardeX does not override managed Claude Code
policies. An environment with detected managed settings, unverifiable policy,
or custom `SSL_CERT_FILE`/`SSL_CERT_DIR` overrides is refused.

The provider token stays in the host. Claude Code gets a short-lived
loopback capability usable only for this attempt's Messages API routes; the
host attaches the token to requests to the configured TLS origin. It checks
all DNS answers before dialing a pinned public IP and retains the original
hostname for TLS verification. A private, local or reserved answer is refused. Parent,
default and channel proxies are not inherited. The relay does not follow or
return redirects or upstream error bodies. Attempt configuration and relay
are removed when the host finishes. This is credential and tool isolation,
not a claim that a local service administrator cannot access host memory or
credentials. The output filter redacts exact token bytes as defense in depth.
A malicious trusted endpoint that encodes or transforms its own bearer is not
covered; configure only a provider you trust.

References: [Claude Code CLI flags](https://code.claude.com/docs/en/cli-reference),
[headless/bare mode](https://code.claude.com/docs/en/headless),
[managed settings locations](https://code.claude.com/docs/en/managed-settings),
[Anthropic SDK beta Messages source](https://github.com/anthropics/anthropic-sdk-typescript/blob/main/src/resources/beta/messages/messages.ts).

### Why developer file tools stay disabled

Claude Code documents `--restricted` working-directory bounds and symlink
checks. Its stronger, explicit file-tool settings are
`permissions.blockReadsOutsideWorkingDirectories` and
`permissions.blockWritesOutsideWorkingDirectories`; the write setting
requires v2.1.314 or later. These settings and deny-on-prompt are better
candidates than an allowlist of tool names, but Claude Code documents
exceptions for some of its own files. Its Bash sandbox applies only to
shell commands, not built-in file tools. CardeX's current macOS seatbelt
allows system reads, network and temporary-area writes, so it does not
independently prove this narrower developer policy. Other CardeX hosts
cannot substitute a sandbox skip for that proof.

Before enabling an opt-in, verify the actual pinned executor and enforced
boundary with a synthetic local endpoint and throwaway directories, never a
provider or real credential. A positive in-worktree read, edit and creation
must succeed while absolute outside paths, traversal, linked paths (including
junctions/aliases where supported), protected metadata and another worktree
remain unchanged and unreadable to the model. Verify no shell, network tool,
configuration or inherited MCP runs. A mock that checks only argv cannot
establish this. The present cloud test environment did not have the pinned
Claude Code binary or a usable process-isolation backend for that test, so
the opt-in is not exposed.

See [Claude Code file-tool permissions](https://code.claude.com/docs/en/permissions#block-file-tool-reads-and-writes-outside-the-working-directories) and
[the Bash sandbox scope](https://code.claude.com/docs/en/sandboxing#how-sandboxing-relates-to-permissions-and-permission-modes).
