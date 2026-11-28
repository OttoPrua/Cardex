# Anthropic-compatible channels (API1)

API1 is configuration-only in this build. Compatible API channels can be
registered, but cannot run cards or send provider requests. This is independent
of config flags and legacy `health=ok` rows. `channel check` reports the hold
without signing in to Claude or contacting the API provider for these channels.

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
      "credential": {"source": "env", "name": "CARDEX_API_GATEWAY_A_TOKEN"}
    }
  }
}
```

Choose exactly one credential reference:

- `env`: the name of a dedicated **CardeX service** environment variable.
  Use a unique `CARDEX_API_*` name, not a legacy key shared with another runner.
  Keep its value unset while compatible runtime is disabled. Git subprocesses (and their
  hooks/helpers), ordinary executors, acceptance commands and notification
  subprocesses drop the reserved
  prefix and configured compatible env references. Keychain service labels
  are not environment variable names. An arbitrary old variable cannot be recovered from a deleted or
  unreadable config; remove any such old credential from the service environment
  before starting the service.
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
Userinfo, query strings, fragments, IP literals and escaped paths are not
accepted. Registration makes no network calls.

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
requires explicitly staging the new identity with `channel set`. A separate
`--health ok` is refused while the runtime is disabled. A health-only
`channel set --id gateway-a --health degraded` disables an old healthy row even
when its config cannot be validated. Repairing that config does not reactivate
the channel. Conflicting positioning families cannot be introduced by either
registration (including the unapproved config seed) or positioning approval.

## Execution boundary

Compatible channels are removed from automatic routing before ranking. An
explicit compatible-only pin waits for input rather than silently sending the
card through a different provider. A historical prepared compatible attempt
is stopped before credentials, a relay, or a child; an unconsumed older
attempt is not harvested through generic acceptance or
`.cardex-result.md`. The no-tool executor's stdout is not an authenticated
workflow result. Ordinary legacy routes continue to use their existing
launchers. If upgrading from an older build, stop old CardeX processes that
could still hold a relay before relying on this boundary.

Future live support requires independent operator authorization, a bounded
result verified end to end, and a guarantee that deactivation or rebinding
prevents new requests on an already started transport. Reference validation
and synthetic isolated-launch tests are not evidence of that capability.

References: [Claude Code CLI flags](https://code.claude.com/docs/en/cli-reference),
[headless/bare mode](https://code.claude.com/docs/en/headless),
[managed settings locations](https://code.claude.com/docs/en/managed-settings).

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

Before enabling any developer opt-in, verify the actual pinned executor and enforced
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
