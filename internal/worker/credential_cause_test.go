package worker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/orpheus-agents/orpheus/internal/session"
)

func TestCredentialCauseNamesTheFailedCheck(t *testing.T) {
	refused := session.Problem(403, "audience_unconfirmed", "Connector did not confirm the run audience.")
	got := credentialCause("kord", "audience", fmt.Errorf("wrapped: %w", refused))
	if got.Code != "audience_unconfirmed" || len(got.Path) != 2 || got.Path[0] != "kord" || got.Path[1] != "audience" {
		t.Fatal("cause", got)
	}
	if problemCode(errors.New("dial tcp 10.0.0.1: private detail")) != "unclassified" {
		t.Fatal("a native error message reached the cause")
	}
	if leaseExpired.Code != "lease_expired" {
		t.Fatal("lease cause", leaseExpired)
	}
}

func TestRenewalRetriesOnlyAnUnavailableProvider(t *testing.T) {
	if !renewalRetryable(fmt.Errorf("call: %w", session.Problem(503, "service_credentials_unavailable", "Service credentials are unavailable."))) {
		t.Fatal("an unavailable provider must be retried while the lease is valid")
	}
	for _, err := range []error{
		session.Problem(403, "service_access_denied", "Provider denied access."),
		session.Problem(403, "audience_unconfirmed", "Connector did not confirm the run audience."),
		session.Problem(409, "service_credentials_conflict", "Invocation context conflicts with the existing grant."),
		errors.New("unknown"),
	} {
		if renewalRetryable(err) {
			t.Fatal("a refusal was retried", err)
		}
	}
}
