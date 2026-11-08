package browserauth

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/orpheus-agents/orpheus/internal/testutil"
)

// TestSAMLAdvisoryGO20266909 exercises the library itself, without HTTP panic
// recovery or database access. The security checker requires this test to run.
func TestSAMLAdvisoryGO20266909(t *testing.T) {
	f := testutil.NewSAML(t, "https://orpheus.example.test")
	s, err := New(f.Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		edit   func(*etree.Element)
		accept bool
	}{
		{name: "valid signed control", accept: true},
		{name: "missing issuer", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:Assertion")
			a.RemoveChild(a.FindElement("saml:Issuer"))
		}},
		{name: "missing subject", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:Assertion")
			a.RemoveChild(a.FindElement("saml:Subject"))
		}},
		{name: "missing conditions", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:Assertion")
			a.RemoveChild(a.FindElement("saml:Conditions"))
		}},
		{name: "missing confirmation data", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:SubjectConfirmation")
			a.RemoveChild(a.FindElement("saml:SubjectConfirmationData"))
		}},
		{name: "missing bearer confirmation", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:Subject")
			a.RemoveChild(a.FindElement("saml:SubjectConfirmation"))
		}},
		{name: "foreign namespace identity", edit: func(e *etree.Element) {
			a := e.FindElement(".//saml:Subject")
			a.RemoveChild(a.FindElement("saml:NameID"))
			n := a.CreateElement("foreign:NameID")
			n.CreateAttr("xmlns:foreign", "urn:untrusted:identity")
			n.SetText("another-user@example.test")
		}},
		{name: "foreign identity cannot override valid identity", accept: true, edit: func(e *etree.Element) {
			n := e.FindElement(".//saml:Subject").CreateElement("foreign:NameID")
			n.CreateAttr("xmlns:foreign", "urn:untrusted:identity")
			n.SetText("another-user@example.test")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const requestID = "_advisory-regression"
			r := httptest.NewRequest("POST", "/auth/callback", strings.NewReader(url.Values{
				"SAMLResponse": {f.Response(t, requestID, tc.edit)},
			}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			a, err := s.sp.ParseResponse(r, []string{requestID})
			if err == nil {
				err = validateAssertion(a, time.Now())
			}
			if !tc.accept {
				if err == nil {
					t.Fatal("malformed signed assertion accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.Subject.NameID.Value != "fixture-subject" {
				t.Fatalf("authenticated a different subject: %q", a.Subject.NameID.Value)
			}
		})
	}
}
