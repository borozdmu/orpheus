// Command vulncheck runs govulncheck with one source-reviewed, expiring correction
// for an upstream advisory with an incorrect open-ended version range.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const (
	samlModule      = "github.com/crewjam/saml"
	samlVersion     = "v0.5.2"
	samlSum         = "h1:C6Md0Q8bI5AtkYeSbnvmJAm77GnoO4HlTuOO9w9V4Sw="
	samlRegression  = "TestSAMLAdvisoryGO20266909"
	samlAdvisorySum = "bb86554c2edddc1d412f6ad363787e5b69a4ca9adb84b143fd62ae6153dea0dd"
)

type frame struct {
	Module, Version, Package, Function string
}

type finding struct {
	OSV   string
	Trace []frame
}

type message struct {
	OSV    json.RawMessage
	Config *struct {
		Scanner string `json:"scanner_name"`
		Level   string `json:"scan_level"`
		Mode    string `json:"scan_mode"`
	}
	Finding *finding
}

// assess keeps govulncheck's default symbol-level policy. Module/package-only
// notices stay visible, but every called vulnerability must be resolved.
func assess(raw []byte, now time.Time, out io.Writer) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	configured, reviewed, blocked, knownRecord := false, false, false, false
	for {
		var m message
		err := decoder.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, fmt.Errorf("invalid scanner report: %w", err)
		}
		if m.Config != nil {
			if configured || m.Config.Scanner != "govulncheck" || m.Config.Level != "symbol" || m.Config.Mode != "source" {
				return false, errors.New("unexpected scanner configuration")
			}
			configured = true
		}
		if len(m.OSV) != 0 {
			var record map[string]any
			if err := json.Unmarshal(m.OSV, &record); err != nil {
				return false, err
			}
			if record["id"] == "GO-2026-6909" {
				canonical, err := json.Marshal(record)
				if err != nil {
					return false, err
				}
				knownRecord = fmt.Sprintf("%x", sha256.Sum256(canonical)) == samlAdvisorySum
			}
		}
		if f := m.Finding; f != nil {
			if f.OSV == "" || len(f.Trace) == 0 || f.Trace[0].Module == "" {
				return false, errors.New("incomplete vulnerability finding")
			}
			v := f.Trace[0]
			if v.Function == "" {
				_, _ = fmt.Fprintf(out, "Dependency notice: %s in %s %s (no called symbol).\n", f.OSV, v.Module, v.Version)
				continue
			}
			_, _ = fmt.Fprintf(out, "Called vulnerability: %s in %s %s %s.%s\n", f.OSV, v.Module, v.Version, v.Package, v.Function)
			if f.OSV == "GO-2026-6909" && v.Module == samlModule && v.Version == samlVersion && now.Before(time.Date(2026, 12, 8, 0, 0, 0, 0, time.UTC)) {
				reviewed = true
			} else {
				blocked = true
			}
		}
	}
	if !configured {
		return false, errors.New("scanner did not produce a complete source report")
	}
	if blocked {
		return false, errors.New("unresolved called vulnerabilities; see govulncheck.json")
	}
	if reviewed && !knownRecord {
		return false, errors.New("SAML advisory changed or is missing; review it before accepting an exception")
	}
	return reviewed, nil
}

func verifySAML() error {
	verify := exec.Command("go", "mod", "verify")
	verify.Stdout, verify.Stderr = os.Stdout, os.Stderr
	if err := verify.Run(); err != nil {
		return fmt.Errorf("dependency source verification failed: %w", err)
	}
	data, err := exec.Command("go", "list", "-m", "-json", samlModule).Output()
	if err != nil {
		return fmt.Errorf("inspect reviewed dependency: %w", err)
	}
	return validateModule(data)
}

func validateModule(data []byte) error {
	var module struct {
		Path, Version, Sum string
		Replace            json.RawMessage
	}
	if err := json.Unmarshal(data, &module); err != nil {
		return err
	}
	if module.Path != samlModule || module.Version != samlVersion || module.Sum != samlSum || len(module.Replace) != 0 {
		return errors.New("SAML source differs from the reviewed version; exception refused")
	}
	return nil
}

func verifyRegression() error {
	cmd := exec.Command("go", "test", "-json", "-count=1", "-run", "^"+samlRegression+"$", "./internal/browserauth")
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	if err != nil {
		_, _ = os.Stdout.Write(data)
		return fmt.Errorf("SAML security regression failed: %w", err)
	}
	return regressionResult(data)
}

func regressionResult(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var event struct{ Action, Test string }
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			return errors.New("required SAML security regression did not run")
		}
		if err != nil {
			return err
		}
		if event.Action == "pass" && event.Test == samlRegression {
			return nil
		}
	}
}

func run() error {
	cmd := exec.Command("govulncheck", "-json", "./...")
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("security scanner failed: %w", err)
	}
	if err := os.WriteFile("govulncheck.json", raw, 0600); err != nil {
		return err
	}
	reviewed, err := assess(raw, time.Now(), os.Stdout)
	if err != nil {
		return err
	}
	if reviewed {
		if err := verifySAML(); err != nil {
			return err
		}
		if err := verifyRegression(); err != nil {
			return err
		}
		fmt.Println("Reviewed false positive GO-2026-6909: exact SAML source checksum and uncached signed-assertion regressions passed. Exception expires 2026-12-08; see docs/security-checks.md.")
	}
	fmt.Println("Security policy passed. The complete unmodified scanner report is saved in govulncheck.json.")
	return nil
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
