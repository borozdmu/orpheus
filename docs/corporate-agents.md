# Corporate agent access

The public `agentcontract` package defines one transport-independent assignment,
its signed audience witness, current-reader audience types and a shared-role
reference loader. The core validates its invocation/author binding; service
scope remains opaque and belongs to the selected Provider.

Configure a service Provider with `protocol = "corporate-assignment.v1"` and
server-only `audience_connectors` keyed by `connector-kind/source-id`. Each entry
has an HTTPS `endpoint` and `secret_env`. The caller cannot select callback URLs.
A single `audience_endpoint` / `audience_secret_env` is also supported for an
isolated, single-connector service. Do not use both forms. Context-field
projections are empty for this protocol. Server keys are excluded from sandbox
and model credential references.

The core confirms the signed task before `issue_assignment`, before renewal,
before model/hooks and every 15 seconds while executing. A failed confirmation
stops protected execution. `POST /api/v1/agent-access/confirm` accepts only a
persisted active invocation, rechecks its connector, and is used by Provider and
mutation gateways before sensitive operations. The endpoint returns no scope
or credential. Terminal/cancelling invocations deny it.

Migration 27 adds `run_service_credentials.revoked_at`. A separate worker pass
revokes terminal or cancelled grants, then records completion. Lost responses
retry the same revoke request after restart; no sandbox needs to remain alive.
Local stop/cleanup does not wait for a remote revoke. Provider scopes, tokens,
absolute deadlines and credentials paths cannot change during renewal.

For corporate access, the sandbox receives `access_protocol` with the existing
run metadata; no server key or callback address is installed. Existing issue /
renew services retain their protocol. Box's release manifest fixes all source
commits and drives one cross-component compatibility test; a matching branch
name is not a compatibility contract.
