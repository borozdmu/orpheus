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

## Trusted input masking

Input masking is disabled by default. An operator can enable it for new sessions
of a selected profile; the public request has no masking override:

```toml
[input_masker]
url = "http://127.0.0.1:3101"
timeout_seconds = 30
# Optional: choose one source, never an inline token.
# token_env = "MASKER_ACCESS_TOKEN"
# token_file = "/run/secrets/masker_access_token"

[profiles.Counsel_beta]
harness = "codex"
model = "your-model"
mask_input = true
[profiles.Counsel_beta.auth]
mode = "api_key"
api_key_env = "COUNSEL_GATEWAY_KEY"
```

Use the trusted service's reachable address (container loopback refers to the
container itself). HTTP and HTTPS are supported; credentials, queries and
fragments in the URL are forbidden. The timeout is required, from 1 to 300 seconds.
Keep this service on the trusted network: it receives original text. Its HMAC key
stays inside the masker; Orpheus uses only `POST /v1/mask/segments`, no manifests.
The optional access token is read from `token_env` or `token_file` and sent only
as `Authorization: Bearer ...`. Missing, empty, malformed or unreadable configured
credentials prevent API startup and fail new admissions without writes. They are
never included in errors, session snapshots, sandbox environment references or
request arguments. Token files may have a trailing newline. When a token is set
with HTTP outside loopback, API startup logs a warning without the token or URL;
HTTP remains allowed for the trusted private network. Rotate the referenced
source without changing accepted requests: new masker calls read its current value.

Before any admission write or credential issuance, Orpheus masks every incoming
message's `text` and the effective initial `agent.instructions` (request override
or profile default). New runs and interactive messages use the immutable
`input_masked` marker in the stored internal session configuration. Changing the
profile affects only new sessions: old ordinary sessions stay ordinary, and
protected sessions continue requiring the masker even after the flag is disabled.

Message `metadata` remains original on the trusted Orpheus database and is never
sent to the agent or sandbox. Technical IDs, routing and access subjects also stay
unchanged. These APIs accept text messages only; attachment fields are rejected by
the existing schema. This feature does not mask tool outputs, hooks or files.

Unavailable service, timeout, non-200, redirect, malformed or incomplete response
returns HTTP 503 `input_masker_unavailable`, with no writes and no original-text
fallback. There are no redirects, environment proxies or automatic retries.
Idempotency hashes the canonical **original request before masking**; only the
digest is persisted, not its original prompt text. An accepted replay returns the
original acceptance before calling the masker, including during an outage or
after profile changes. A changed request with the same key returns 409.

To stop using the feature, disable the profile flag and create new sessions.
Do not resume protected sessions with a binary that predates this feature.
