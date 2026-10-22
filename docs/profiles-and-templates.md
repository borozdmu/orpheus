# Profiles and templates

Define agent profiles and allowed sandbox templates in `orpheus.toml`.
For example, a complete configuration with one profile and one template:

```toml
[profiles.research]
description = "Research and analysis"
harness = "codex"
model = "your-model"

[profiles.research.auth]
mode = "api_key"
api_key_env = "OPENAI_API_KEY"

[templates.orpheus-codex]
description = "Codex sandbox"
```

At least one template is required. Each key is the exact AgentBox template name,
including any tag; registration does not create or check the template in AgentBox.
Set optional `description` under `[profiles.<name>]` to describe a profile.
API and worker must use the same configuration file; restart them after changes.

`GET /api/v1/profiles` and `GET /api/v1/templates` return complete catalogs as
`{"items": [...]}`, sorted by name, with the usual read authentication.
Profiles expose `name`, `description`, `harness`, `model`, `codex` and
`instructions`; templates expose `name` and `description`. Missing, empty or
whitespace-only descriptions and missing models are `null`; omitted Codex options
stay absent. Authentication settings, credential locations and ENV values are
excluded. Descriptions and instructions are public configuration and must not
contain secrets.

New sessions reject unregistered names with HTTP 422 (`unknown_profile` or
`unknown_template`). Removing a name does not change accepted sessions or their
idempotent replays. Descriptions are not part of execution snapshots.

## Codex settings

Optional Codex settings belong under `[profiles.default.codex]`:

```toml
[profiles.default.codex]
effort = "medium"
summary = "auto"
personality = "friendly"
service_tier = "default"
```

| Setting | Values |
| --- | --- |
| `effort` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra` |
| `summary` | `auto`, `concise`, `detailed`, `none` |
| `personality` | `none`, `friendly`, `pragmatic` |
| `service_tier` | A nonblank tier name accepted by the Codex provider, such as `default` |

Each setting is optional; omitted settings are not sent to Codex. Explicit empty
values are rejected. Model and account support still determine which options
Codex can use. Settings are captured when a session is created and reused after
resume; changing the profile affects new sessions. They are profile settings,
not public API overrides. Connectors select them through `agent.profile`.
# HTTPS gateway profile

An operator may set `auth.base_url` on an `api_key` profile. Only absolute HTTPS
URLs without user information, query or fragment are accepted. The public session
API cannot override this URL. `account` profiles reject this field.

```toml
[profiles.gateway]
harness = "codex"
model = "gpt-6-luna"
[profiles.gateway.codex]
effort = "high"
[profiles.gateway.auth]
mode = "api_key"
api_key_env = "MY_GATEWAY_KEY"
base_url = "https://gateway.example/v1"
```

Supply only the gateway credential in `MY_GATEWAY_KEY`. Codex receives it as the
reserved `COUNSEL_GATEWAY_KEY` launch environment variable. Its dedicated provider
uses Responses over HTTPS, disables WebSockets and retries, and performs no
`account/login/start`. Its URL is written to the sandbox's `CODEX_HOME/config.toml`
using TOML serialization; neither the credential nor the URL is in launch argv.
Use a separate sandbox and state directory for this profile: its launch replaces
that directory's `config.toml` with the operator's provider settings. The caller's
environment map is unchanged. Missing credentials prevent launch.

Without `base_url`, API-key login and account login retain their previous behavior.
Stored session configurations without the new field remain readable. This routes
Codex requests; it does not restrict the sandbox's other network access.
