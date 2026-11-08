package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const configReport = `{"config":{"scanner_name":"govulncheck","scan_level":"symbol","scan_mode":"source"}}`

func findingReport(id, module, version, function string) string {
	data, _ := json.Marshal(message{Finding: &finding{OSV: id, Trace: []frame{{Module: module, Version: version, Package: module, Function: function}}}})
	return string(data)
}

func TestAssessmentFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	known := findingReport("GO-2026-6909", samlModule, samlVersion, "ParseResponse")
	other := findingReport("GO-2099-0001", samlModule, samlVersion, "ParseResponse")
	record, err := os.ReadFile("testdata/GO-2026-6909.json")
	if err != nil {
		t.Fatal(err)
	}
	reviewedConfig := configReport + `{"osv":` + string(record) + `}`
	for _, tc := range []struct {
		name, report     string
		reviewed, reject bool
	}{
		{name: "clean", report: configReport},
		{name: "reviewed exact version", report: reviewedConfig + known, reviewed: true},
		{name: "missing advisory", report: configReport + known, reject: true},
		{name: "changed advisory", report: strings.Replace(reviewedConfig, "namespace-blind", "new defect", 1) + known, reject: true},
		{name: "different advisory", report: configReport + other, reject: true},
		{name: "additional vulnerability", report: configReport + known + other, reject: true},
		{name: "old vulnerable SAML", report: configReport + findingReport("GO-2026-6909", samlModule, "v0.5.1", "ParseResponse"), reject: true},
		{name: "unreviewed newer SAML", report: configReport + findingReport("GO-2026-6909", samlModule, "v0.6.3", "ParseResponse"), reject: true},
		{name: "different module", report: configReport + findingReport("GO-2026-6909", "example.test/saml", samlVersion, "ParseResponse"), reject: true},
		{name: "uncalled module notice", report: configReport + findingReport("GO-2099-0001", "example.test/unused", "v1.0.0", "")},
		{name: "missing report", reject: true},
		{name: "missing configuration", report: known, reject: true},
		{name: "malformed tail", report: configReport + known + "{", reject: true},
		{name: "duplicate configuration", report: configReport + configReport, reject: true},
		{name: "weaker scan", report: strings.Replace(configReport, "symbol", "module", 1), reject: true},
		{name: "incomplete finding", report: configReport + `{"finding":{"osv":"GO-2099-0001"}}`, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			reviewed, err := assess([]byte(tc.report), now, &out)
			if (err != nil) != tc.reject || reviewed != tc.reviewed {
				t.Fatalf("reviewed=%v, error=%v; output=%s", reviewed, err, out.String())
			}
		})
	}
	for _, now := range []time.Time{
		time.Date(2026, 12, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		if _, err := assess([]byte(configReport+known), now, &bytes.Buffer{}); err == nil {
			t.Fatal("expired exception accepted", now)
		}
	}
}

func TestReviewedSourceCannotBeReplaced(t *testing.T) {
	valid := `{"Path":"` + samlModule + `","Version":"` + samlVersion + `","Sum":"` + samlSum + `"}`
	if err := validateModule([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		strings.Replace(valid, samlVersion, "v0.5.1", 1),
		strings.Replace(valid, samlSum, "h1:unreviewed", 1),
		strings.Replace(valid, samlModule, "example.test/fork", 1),
		strings.TrimSuffix(valid, "}") + `,"Replace":{"Path":"../saml"}}`,
		`{`,
	} {
		if err := validateModule([]byte(data)); err == nil {
			t.Fatal("unreviewed source accepted", data)
		}
	}
}

func TestRegressionMustActuallyPass(t *testing.T) {
	valid := `{"Action":"pass","Test":"` + samlRegression + `"}`
	if err := regressionResult([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		``,
		`{"Action":"pass"}`,
		strings.Replace(valid, "pass", "skip", 1),
		strings.Replace(valid, "pass", "fail", 1),
		strings.Replace(valid, samlRegression, samlRegression+"/subtest", 1),
		`{`,
	} {
		if err := regressionResult([]byte(data)); err == nil {
			t.Fatal("missing or failing regression accepted", data)
		}
	}
}
