package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orpheus-agents/orpheus/internal/harness"
	"github.com/orpheus-agents/orpheus/internal/session"
	"github.com/pelletier/go-toml/v2"
)

func TestGatewayPreservesTemplateConfig(t *testing.T) {
	t.Setenv("PR7_REVIEW_GATEWAY_KEY", "synthetic-gateway-key")
	box := gatewayFilesystem(t)
	path := filepath.Join(box.home, "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.company]\nurl='https://tools.example/mcp'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := New(box, time.Second, 1024)
	defer func() { _ = d.Close() }()
	source := session.Credentials{Mode: "api_key", APIKeyEnv: "PR7_REVIEW_GATEWAY_KEY", BaseURL: "https://gateway.example/v1"}
	if err := d.PrepareLaunch(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Launch(t.Context(), nil, "/workspace", source); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcp_servers.company") {
		t.Fatal("existing template MCP configuration was erased by gateway launch")
	}
}

// Exercise the actual shell rename and filesystem rather than simulating them.
type gatewayFilesystemBox struct {
	*testBox
	home       string
	startCalls int
	write      func(context.Context, string, []byte) error
	runCommand func(context.Context, string) ([]byte, error)
}

func (b *gatewayFilesystemBox) Start(ctx context.Context, command string, env map[string]string, cwd string) (harness.Stream, int, error) {
	b.startCalls++
	return b.testBox.Start(ctx, command, env, cwd)
}

func TestGatewayLaunchRequiresMatchingPreparation(t *testing.T) {
	t.Setenv("GATEWAY_GUARD_KEY", "synthetic-gateway-key")
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "unprepared", true: "different_base_url"}[prepared], func(t *testing.T) {
			box := gatewayFilesystem(t)
			d := New(box, time.Second, 1024)
			defer func() { _ = d.Close() }()
			source := session.Credentials{Mode: "api_key", APIKeyEnv: "GATEWAY_GUARD_KEY", BaseURL: "https://gateway.example/v1"}
			if prepared {
				if err := d.PrepareLaunch(t.Context(), source); err != nil {
					t.Fatal(err)
				}
				source.BaseURL = "https://other-gateway.example/v1"
			}
			pid, err := d.Launch(t.Context(), nil, "/workspace", source)
			failure, ok := errors.AsType[*harness.ExecutionError](err)
			if !ok || failure.Code != "environment_unavailable" || pid != 0 || box.startCalls != 0 {
				t.Fatalf("unprepared gateway started: pid=%d starts=%d error=%v", pid, box.startCalls, err)
			}
		})
	}
}

func TestGatewayRejectsUnsafeTemplateConfig(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"selected_profile", "profile='direct'\n"},
		{"empty_profile", "profile=''\n"},
		{"profile_provider", "[profiles.direct]\nmodel_provider='direct'\n"},
		{"empty_profile_provider", "[profiles.direct]\nmodel_provider=''\n"},
		{"mcp_env_vars", "[mcp_servers.company]\nenv_vars=['COUNSEL_GATEWAY_KEY']\n"},
		{"mcp_env_name", "[mcp_servers.company.env]\nCOUNSEL_GATEWAY_KEY='synthetic-value'\n"},
		{"mcp_env_value", "[mcp_servers.company.env]\nOTHER='${COUNSEL_GATEWAY_KEY}'\n"},
		{"mcp_string", "[mcp_servers.company]\ncommand='echo $COUNSEL_GATEWAY_KEY'\n"},
		{"mcp_nested_values", "[mcp_servers.company]\nargs=[{nested=['${COUNSEL_GATEWAY_KEY}']}]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := gatewayFilesystem(t)
			path := filepath.Join(box.home, "config.toml")
			if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			d := New(box, time.Second, 1024)
			defer func() { _ = d.Close() }()
			source := session.Credentials{Mode: "api_key", BaseURL: "https://gateway.example/v1"}
			err := d.PrepareLaunch(t.Context(), source)
			failure, ok := errors.AsType[*harness.ExecutionError](err)
			if !ok || failure.Code != "environment_unavailable" {
				t.Fatalf("unsafe gateway template accepted: %v", err)
			}
			current, err := os.ReadFile(path)
			if err != nil || string(current) != tc.config || len(box.files) != 0 {
				t.Fatal("rejected template changed", err)
			}
			if _, err := d.Launch(t.Context(), nil, "/workspace", source); err == nil || box.startCalls != 0 {
				t.Fatal("Start allowed after failed preparation", err)
			}
		})
	}
}

func gatewayFilesystem(t *testing.T) *gatewayFilesystemBox {
	t.Helper()
	return &gatewayFilesystemBox{testBox: &testBox{stream: &testStream{out: make(chan []byte), errout: make(chan []byte)}}, home: t.TempDir()}
}
func (b *gatewayFilesystemBox) Run(ctx context.Context, command string) ([]byte, error) {
	if b.runCommand != nil {
		return b.runCommand(ctx, command)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+b.home)
	return cmd.Output()
}
func (b *gatewayFilesystemBox) Read(_ context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}
func (b *gatewayFilesystemBox) Write(ctx context.Context, path string, raw []byte) error {
	if b.write != nil {
		return b.write(ctx, path, raw)
	}
	if err := b.testBox.Write(ctx, path, raw); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
func TestGatewayConfigRepeatAndRecovery(t *testing.T) {
	const original = `model = "template-model"
model_provider = "other"
[features]
my_feature = true
[mcp_servers.company]
url = "https://tools.example/mcp"
env_vars = ["OTHER"]
startup_timeout_sec = 10
[profiles.only_model]
model = "profile-model"
[model_providers.other]
name = "Other"
[model_providers.counsel_gateway]
http_headers = { Authorization = "obsolete" }
`
	box := gatewayFilesystem(t)
	path := filepath.Join(box.home, "config.toml")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	source := session.Credentials{Mode: "api_key", BaseURL: "https://gateway.example/v1"}
	var previous map[string]any
	for range 3 {
		d := New(box, time.Second, 1024)
		if err := d.PrepareLaunch(t.Context(), source); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := toml.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		if config["model"] != "template-model" || config["features"].(map[string]any)["my_feature"] != true || !strings.Contains(string(raw), "mcp_servers.company") || !strings.Contains(string(raw), "model_providers.other") || strings.Contains(string(raw), "obsolete") {
			t.Fatal("template changed or stale auth retained")
		}
		profiles := config["profiles"].(map[string]any)
		if profiles["only_model"].(map[string]any)["model"] != "profile-model" {
			t.Fatal("compatible profile setting lost")
		}
		if previous != nil && !reflect.DeepEqual(previous, config) {
			t.Fatal("preparation is not idempotent after reconnect")
		}
		previous = config
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe config permissions", err)
		}
		_ = d.Close()
	}
}
func TestGatewayConfigAtomicWriteAndLostRenameResponse(t *testing.T) {
	for _, loss := range []string{"write", "rename"} {
		t.Run(loss, func(t *testing.T) {
			box := gatewayFilesystem(t)
			path := filepath.Join(box.home, "config.toml")
			original := []byte("[mcp_servers.company]\nurl='https://tools.example/mcp'\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			source := session.Credentials{Mode: "api_key", BaseURL: "https://gateway.example/v1"}
			d := New(box, time.Second, 1024)
			if loss == "write" {
				box.write = func(_ context.Context, temp string, raw []byte) error {
					if err := os.WriteFile(temp, raw[:len(raw)/2], 0600); err != nil {
						t.Fatal(err)
					}
					current, err := os.ReadFile(path)
					if err != nil || string(current) != string(original) {
						t.Fatal("partial config visible", err)
					}
					return errors.New("lost write response")
				}
			} else {
				box.runCommand = func(ctx context.Context, command string) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "sh", "-c", command)
					cmd.Env = append(os.Environ(), "CODEX_HOME="+box.home)
					out, err := cmd.Output()
					if strings.Contains(command, "mv -f") && err == nil {
						return nil, errors.New("lost rename response")
					}
					return out, err
				}
			}
			if err := d.PrepareLaunch(t.Context(), source); err == nil {
				t.Fatal("fault not injected")
			}
			box.write, box.runCommand = nil, nil
			_ = d.Close()
			recovered := New(box, time.Second, 1024)
			if err := recovered.PrepareLaunch(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(raw), "mcp_servers.company") || !strings.Contains(string(raw), "counsel_gateway") {
				t.Fatal("lost template on recovery", err)
			}
			_ = recovered.Close()
		})
	}
}

func TestOrdinaryProfilesDoNotPrepareGatewayConfig(t *testing.T) {
	for _, mode := range []string{"api_key", "account"} {
		t.Run(mode, func(t *testing.T) {
			box := gatewayFilesystem(t)
			path := filepath.Join(box.home, "config.toml")
			original := []byte("[mcp_servers.company]\nurl='https://tools.example/mcp'\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			box.runCommand = func(context.Context, string) ([]byte, error) {
				t.Error("ordinary preparation performed sandbox I/O")
				return nil, errors.New("unexpected preparation")
			}
			d := New(box, time.Second, 1024)
			defer func() { _ = d.Close() }()
			source := session.Credentials{Mode: mode, APIKeyEnv: "KEY"}
			if err := d.PrepareLaunch(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Launch(t.Context(), nil, "/workspace", source); err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(path)
			if err != nil || string(current) != string(original) || box.startEnv["COUNSEL_GATEWAY_KEY"] != "" {
				t.Fatal("ordinary configuration or environment changed", err)
			}
		})
	}
}
