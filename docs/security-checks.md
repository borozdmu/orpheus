# Security checks

Run `make vuln` for the enforced security policy and Trivy. It scans the entire
application with unmodified `govulncheck` against the public Go vulnerability
database. New called vulnerabilities, scanner errors and malformed reports fail
the check. Module/package-only notices retain govulncheck's default nonblocking
behavior. The complete, unmodified JSON report is saved in `govulncheck.json` and
uploaded by CI. `make vuln-raw` runs the original text command without policy
assessment; it currently exits nonzero for the reviewed finding below.

The integration also updates `golang.org/x/net` to v0.62.0 to address
[GO-2026-6860 / CVE-2026-78661](https://pkg.go.dev/vuln/GO-2026-6860),
reported by Trivy against the deployed v0.60.0 dependency.

## Reviewed false positive: GO-2026-6909

The shared-runtime integration pins `github.com/crewjam/saml` to v0.5.2.
The reviewed upstream tag resolves to
[`400b0e48aad125f09de4627eae3245985c8275fc`](https://github.com/crewjam/saml/commit/400b0e48aad125f09de4627eae3245985c8275fc).
It binds `Subject.NameID` to the SAML assertion namespace and rejects assertions
without the required subject and conditions. Our browser-auth integration tests
submit signed responses with those elements missing and with a foreign-namespace
NameID; all must return a refusal instead of authenticating a different subject
or panicking.

The Go advisory [GO-2026-6909](https://pkg.go.dev/vuln/GO-2026-6909) currently has
no fixed-version event, although its description and the
[original report](https://github.com/golang/vulndb/issues/6909) describe versions
through v0.5.1. Consequently `govulncheck` still reports v0.5.2 and returns a
nonzero exit code. Keep that result visible until the upstream advisory is
corrected. The policy prints the original finding and its reviewed disposition.

The [v0.5.1...v0.5.2 comparison](https://github.com/crewjam/saml/compare/v0.5.1...v0.5.2)
adds the missing nil guards and assertion-namespace bindings. The issuer is a
value field; the actual nil checks concern Subject, Conditions and
SubjectConfirmationData. The upstream discussion documents this description
error too.

The temporary policy accepts only GO-2026-6909 for the exact v0.5.2 module and
module checksum, with no local replacement. Before accepting the exception,
the checker verifies the module cache with `go mod verify`, then reruns
`TestSAMLAdvisoryGO20266909` without the test cache and requires
an explicit passing result. This suite invokes the real library on freshly
signed responses, outside HTTP panic recovery and without a database. It tests
missing issuer/subject/conditions/confirmation data, missing bearer confirmation,
foreign-namespace identity and valid controls preserving the real subject.
End-to-end browser-login tests still cover refusals and session creation.

The exception expires at **2026-12-08 00:00 UTC**. A different version, modified
source, failing/missing regression, expired review, or any other called
vulnerability fails the check. Once the upstream database records v0.5.2 as fixed,
remove this temporary policy and restore the direct `govulncheck` command. A
passing policy result must not be described as a clean raw scanner result.

The policy also fingerprints the reviewed advisory record. If its contents change
while it still reports this version, the check requires a fresh review instead
of silently accepting a potentially expanded vulnerability.

## External service tests

CI always runs unit, database integration, migration, race, security and production
smoke checks. Live AgentBox/OpenAI tests additionally require the repository
secrets `AGENTBOX_API_KEY` and `OPENAI_API_KEY`. The configuration job checks only
their presence. Without both keys, it records the missing setup in the run
summary and marks the live job skipped. A configured live job runs the unchanged
`make test-live` command; its failures remain blocking. No production credentials
are copied into GitHub automatically.
