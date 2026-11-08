# Security check evidence

The integration also updates `golang.org/x/net` to v0.62.0 to address
[GO-2026-6860 / CVE-2026-78661](https://pkg.go.dev/vuln/GO-2026-6860),
reported by Trivy against the deployed v0.60.0 dependency.

The shared-runtime integration pins `github.com/crewjam/saml` to v0.5.2.
The upstream tag resolves to
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
corrected. No vulnerability finding is filtered or suppressed by this repository.

Run `make vuln` for the unmodified security checks and `make test-integration`
for signed assertion validation. An overall `make check` result remains nonzero
while the advisory is reported; successful regression tests must not be described
as a clean scanner result.
