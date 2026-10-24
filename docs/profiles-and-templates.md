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
Use a separate sandbox and state directory for this profile. Before process
launch, Orpheus preserves the template's TOML settings (including MCP and other
providers), replaces the reserved `model_providers.counsel_gateway` table and
sets `model_provider`. Comments and formatting may change during serialization.
Gateway templates must not set top-level `profile`, any `profiles.*.model_provider`, or reference `COUNSEL_GATEWAY_KEY` anywhere in MCP settings (including keys, nested values, `env` and `env_vars`); preparation rejects these configurations before writing or starting Codex.
Preparation has a timeout of at most 30 seconds (`RPC_TIMEOUT_SECONDS` if lower)
and a 1 MiB configuration size limit. Invalid or unreadable existing TOML fails
closed. The new configuration is uploaded to a private temporary file and renamed
over `config.toml` in the same directory. A preparation failure finishes the run
and releases its slot through normal sandbox settlement; a worker shutdown before
process Start leaves preparation safely repeatable on restart. A lost response
after Start still requires process discovery, never a blind second Start. The caller's
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
credentials leave the shared API available and fail protected admissions without
writes. Ordinary profiles remain available. A startup warning and sanitized
admission warnings diagnose the failure. The existing internal
`/metrics/service` endpoint reports `orpheus_input_masker_secret_available`
(0 or 1, absent without masker configuration); it reads the current secret but
does not probe HTTP reachability. `/ready` and `/health` remain process probes.
Credentials are
never included in errors, session snapshots, sandbox environment references or
request arguments. Token files may have a trailing newline. For any HTTP address
outside loopback, API startup logs a warning without the token or URL, even when
no access token is configured;
HTTP remains allowed for the trusted private network. Rotate the referenced
source without changing accepted requests: new masker calls read its current value.

Before any admission write or credential issuance, Orpheus masks every incoming
message's `text` and initial `agent.instructions` supplied by the client.
Static operator instructions from the profile remain unchanged and are never
sent to the masker. New runs and interactive messages use the immutable
`input_masked` marker in the stored internal session configuration. Changing the
profile affects only new sessions: old ordinary sessions stay ordinary, and
protected sessions continue requiring the masker even after the flag is disabled.

Message `metadata` remains original on the trusted Orpheus database and is never
sent to the agent or sandbox. Technical IDs, routing and access subjects also stay
unchanged. These APIs accept text messages only; attachment fields are rejected by
the existing schema. Values in `configuration.sandbox.env`, request/run `env`
and hook script bodies are also trusted-client fields and are **not masked**;
the sandbox and agent may read them. Clients must not put unmasked sensitive text
in those fields. This feature does not mask tool outputs or files either.

The Telegram connector supplies workflow instructions as client instructions
(`orpheus-telegram/internal/service/engine.go:573`, `:577`), so Counsel_beta's
workflow prompt will be masked. Checking the actual Counsel_beta prompt through
the real masker belongs to MVP step 3; this intake step uses synthetic prompts.

Unavailable service, timeout, non-200, redirect, malformed or incomplete response
returns HTTP 503 `input_masker_unavailable`, with no writes and no original-text
fallback. There are no redirects, environment proxies or automatic retries.
Clients must retry HTTP 503 `input_masker_unavailable` with the same request body
and idempotency key, including when the masker is busy (429 becomes 503).
The Telegram connector does retry: non-2xx becomes an error
(`orpheus-telegram/internal/orpheus/client.go:56`); 503 retains the pending request
(`internal/service/engine.go:735`, `:746`); the reconcile loop submits it again
(`internal/service/engine.go:373`, `internal/service/runtime.go:33`, `:109`).
This was verified by reading source, not a live connector test.
Idempotency hashes the canonical **original request before masking**; only the
digest is persisted, not its original prompt text. An accepted replay returns the
original acceptance before calling the masker, including during an outage or
after profile changes. Token and masker failures recheck durable replay with the
original digest before returning 503, so a concurrent committed request wins.
A changed request with the same key returns 409.

### Rollout and rollback gate

Old binaries ignore the stored privacy marker and can persist plaintext. This
release adds no database fence that can stop an already released binary. **Do not
roll out with mixed versions while any protected session can be addressed.**
The gate below is a deployment check, not a security boundary against an operator
who supplies an incomplete inventory or restarts an old workload afterwards.

1. Freeze deployment, autoscaling, rollback automation and restart of old
   workloads for every host with access to this database. Inventory all API and
   worker processes, including stopped replicas and non-Docker processes. Identify
   the exact database/queue scope; do not infer it from a container name.
2. With `mask_input` disabled everywhere, stop all old API and worker processes.
   Build the approved commit into one immutable image, with the image label
   `org.opencontainers.image.revision=<full commit SHA>`. Record its tested
   `sha256:` image ID on each Docker engine; `orpheus --version` alone is not a
   revision check. Remove stopped old replicas and prevent their recreation.
   Start only the approved API and workers. Set container labels
   `io.orpheus.database-scope=<non-secret fleet ID>` and `io.orpheus.role=api`
   or `worker` after verifying their actual database configuration privately.
3. On a Docker-only deployment, run the read-only preflight, listing **every**
   Docker context that can reach this database. Use the actual full SHA and image
   ID recorded above, not a mutable tag:

   ```sh
   python3 tools/masking-preflight.py \
     --context engine-a --context engine-b --scope shared-orpheus \
     --revision "$APPROVED_COMMIT" --image "$TESTED_IMAGE_ID" \
     --exclusive-docker-fleet
   ```

   The last flag attests that all database-capable processes are in those frozen
   contexts. The command requires running current API and worker containers,
   checks the image revision and ID, rejects old scoped images even if stopped,
   and rejects recognizable unscoped Orpheus containers. It reads no container
   environment, configuration or logs. Unknown/unreachable inventory fails closed.
   It cannot detect a hidden binary, a false scope label, an omitted engine, a
   nested process or a future restart. If exclusive Docker inventory cannot be
   established, **stop here** and require an equivalent verified supervisor
   inventory and database-access restriction before enabling masking.
4. Check current DB connections (`pg_stat_activity`) against that inventory and
   confirm no other DB-capable service account, host or queued deployment can
   start an old API/worker. A momentary connection list alone cannot exclude an
   idle old process. Check ordinary api_key/account agents and continuation of
   existing ordinary sessions with the new image; verify MCP and template/env
   compatibility. Keep masking off if any check is incomplete.
5. Only after all gates pass, enable one protected profile and restart its API
   configuration using the approved image. Keep the deployment freeze and
   observe ordinary admissions, occupied slots and masker errors. Record the
   exact revision, image, contexts, results and time. Rerun preflight after any
   replica/image change before admitting protected traffic.

To stop using the feature, disable the profile flag and create new sessions.
Existing protected sessions retain their policy. For a binary rollback, first
stop protected admission and execution, then inventory protected sessions on the
trusted database:

```sql
SELECT id FROM sessions WHERE configuration->>'input_masked' = 'true';
```

If any row remains, the old binary must not access this database, including
terminal reusable sessions. Roll back to a version supporting the marker, or
prepare an explicitly approved separate database without protected sessions;
preserve ordinary session IDs/state through that separately reviewed procedure.
Do not erase privacy markers or original history to pass the check. Only when
the target database has zero protected sessions and all protected entrypoints
are stopped may an old binary start. Its strict TOML decoder also requires
removing `[input_masker]` and all profile `mask_input` fields, but that removal
alone does not make stored protected sessions safe.
