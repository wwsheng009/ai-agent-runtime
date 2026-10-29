package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/mcpmount"
)

func TestLoadOrCreateMountIdentityIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-agent", "mount-id")

	first, err := LoadOrCreateMountIdentity(path)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if len(first) < 16 {
		t.Fatalf("identity %q is too short", first)
	}
	second, err := LoadOrCreateMountIdentity(path)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if first != second {
		t.Fatalf("identity changed across calls: %q -> %q", first, second)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("identity file missing or empty: %v", err)
	}

	// An empty file is a corrupted identity, not a reason to silently rotate the
	// public URL: fail closed so the operator can restore the value.
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("truncate identity: %v", err)
	}
	if _, err := LoadOrCreateMountIdentity(path); err == nil {
		t.Fatalf("empty identity file was accepted")
	}
}

func TestBuildMountRegistrationResolvesSelfAndExtras(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profile = ProfileStandard
	cfg.ToolPrefix = "remote_prod"
	cfg.TargetName = "prod"
	cfg.Workspace = "/srv/app"
	cfg.HTTP.Path = "/mcp"
	cfg.HTTP.TokenEnv = "AICLI_REMOTE_TOKEN"
	cfg.Gateway = GatewayConfig{
		URL:      "wss://gw.example.com",
		SelfName: "prod-readonly",
		Mounts: []GatewayMountConfig{{
			Name:         "k8s",
			Upstream:     "http://127.0.0.1:18766/mcp",
			TokenEnv:     "K8S_TOKEN",
			Timeout:      Duration{Duration: 15 * time.Minute},
			MaxBodyBytes: 1 << 20,
			MaxConcurren: 4,
		}},
	}

	registration, err := BuildMountRegistration(cfg, "identity-1", "127.0.0.1:18765")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(registration.Specs) != 2 || len(registration.Forwards) != 2 {
		t.Fatalf("specs/forwards = %d/%d, want 2/2", len(registration.Specs), len(registration.Forwards))
	}
	if len(registration.Routes) != 1 || registration.Routes[0] != "/mcp" {
		t.Fatalf("routes = %v, want deduped [/mcp]", registration.Routes)
	}

	self := registration.Specs[0]
	if want := mcpmount.DeriveSlug("identity-1", "prod-readonly"); self.Slug != want {
		t.Fatalf("self slug = %q, want %q", self.Slug, want)
	}
	if self.Upstream != "http://127.0.0.1:18765/mcp" {
		t.Fatalf("self upstream = %q", self.Upstream)
	}
	if self.Auth == nil || self.Auth.TokenEnv != "AICLI_REMOTE_TOKEN" || self.Auth.Type != "bearer" {
		t.Fatalf("self auth = %+v", self.Auth)
	}
	if self.Limits == nil || self.Limits.TimeoutSec != int((10*time.Minute)/time.Second) || self.Limits.MaxBodyBytes != 8<<20 {
		t.Fatalf("self limits = %+v", self.Limits)
	}
	if self.Labels == nil || self.Labels.Profile != string(ProfileStandard) || self.Labels.ToolPrefix != "remote_prod" {
		t.Fatalf("self labels = %+v", self.Labels)
	}
	if forward := registration.Forwards[0]; forward.BaseURL != "http://127.0.0.1:18765" || forward.TokenEnv != "AICLI_REMOTE_TOKEN" {
		t.Fatalf("self forward = %+v", forward)
	}

	extra := registration.Specs[1]
	if extra.Name != "k8s" || extra.Upstream != "http://127.0.0.1:18766/mcp" {
		t.Fatalf("extra spec = %+v", extra)
	}
	if extra.Auth == nil || extra.Auth.TokenEnv != "K8S_TOKEN" {
		t.Fatalf("extra auth = %+v", extra.Auth)
	}
	if extra.Limits == nil || extra.Limits.TimeoutSec != 900 || extra.Limits.MaxBodyBytes != 1<<20 || extra.Limits.MaxConcurrency != 4 {
		t.Fatalf("extra limits = %+v", extra.Limits)
	}
	// Slugs are deterministic, so a restart keeps every client URL.
	again, err := BuildMountRegistration(cfg, "identity-1", "127.0.0.1:18765")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if again.Specs[0].Slug != self.Slug || again.Specs[1].Slug != extra.Slug {
		t.Fatalf("slugs changed across builds: %+v vs %+v", again.Specs, registration.Specs)
	}
}

func TestBuildMountRegistrationWildcardListener(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HTTP.Path = "/mcp"
	cfg.Gateway = GatewayConfig{URL: "wss://gw", MountSelf: boolPtr(true)}

	registration, err := BuildMountRegistration(cfg, "identity-1", "0.0.0.0:18765")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := registration.Specs[0].Upstream; got != "http://127.0.0.1:18765/mcp" {
		t.Fatalf("wildcard listen upstream = %q, want loopback", got)
	}

	cfg.HTTP.TLSCert = "cert.pem"
	cfg.HTTP.TLSKey = "key.pem"
	registration, err = BuildMountRegistration(cfg, "identity-1", "[::]:18765")
	if err != nil {
		t.Fatalf("tls build: %v", err)
	}
	if got := registration.Specs[0].Upstream; got != "https://127.0.0.1:18765/mcp" {
		t.Fatalf("tls upstream = %q", got)
	}
}

func TestBuildMountRegistrationRejectsDuplicates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HTTP.Path = "/mcp"
	cfg.Gateway = GatewayConfig{
		URL:       "wss://gw",
		MountSelf: boolPtr(false),
		Mounts: []GatewayMountConfig{
			{Name: "a", Slug: "same-slug", Upstream: "http://127.0.0.1:1/mcp"},
			{Name: "b", Slug: "same-slug", Upstream: "http://127.0.0.1:2/mcp"},
		},
	}
	if _, err := BuildMountRegistration(cfg, "identity-1", "127.0.0.1:18765"); err == nil {
		t.Fatalf("duplicate slug was accepted")
	}
}

func TestNormalizeGateway(t *testing.T) {
	workspace := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = workspace
	cfg.Gateway = GatewayConfig{URL: " wss://gw ", PathStyle: "ROOT"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Gateway.URL != "wss://gw" || cfg.Gateway.TokenEnv != DefaultGatewayTokenEnv {
		t.Fatalf("gateway defaults = %+v", cfg.Gateway)
	}
	if cfg.Gateway.PathStyle != mcpmount.PathStyleRoot {
		t.Fatalf("path style = %q", cfg.Gateway.PathStyle)
	}

	// Disabled block with no mounts is fine.
	disabled := DefaultConfig()
	disabled.Workspace = workspace
	disabled.Gateway = GatewayConfig{}
	if err := disabled.Normalize(); err != nil {
		t.Fatalf("disabled normalize: %v", err)
	}

	bad := DefaultConfig()
	bad.Workspace = workspace
	bad.Gateway = GatewayConfig{URL: "wss://gw", Mounts: []GatewayMountConfig{{Upstream: "http://127.0.0.1:1/mcp"}}}
	if err := bad.Normalize(); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("missing name error = %v", err)
	}

	badAuth := DefaultConfig()
	badAuth.Workspace = workspace
	badAuth.Gateway = GatewayConfig{URL: "wss://gw", Mounts: []GatewayMountConfig{{
		Name: "x", Upstream: "http://127.0.0.1:1/mcp", AuthType: "oauth",
	}}}
	if err := badAuth.Normalize(); err == nil || !strings.Contains(err.Error(), "authType") {
		t.Fatalf("bad auth error = %v", err)
	}
}

func TestGatewayInstanceDerivesIdentityAndNames(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Gateway = GatewayConfig{URL: "wss://gw", Instance: " Backend-01 "}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Gateway.Instance != "backend-01" {
		t.Fatalf("instance = %q, want normalized backend-01", cfg.Gateway.Instance)
	}
	want, err := InstanceIdentityPath("backend-01")
	if err != nil {
		t.Fatalf("InstanceIdentityPath: %v", err)
	}
	if cfg.Gateway.IdentityFile != want {
		t.Fatalf("identity = %q, want %q", cfg.Gateway.IdentityFile, want)
	}
	if cfg.Gateway.SelfName != "backend-01" || cfg.Gateway.Name != "backend-01" {
		t.Fatalf("default labels = selfName %q, name %q", cfg.Gateway.SelfName, cfg.Gateway.Name)
	}
}

func TestGatewayInstanceKeepsExplicitIdentityAndLabels(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.Gateway = GatewayConfig{
		URL:          "wss://gw",
		Instance:     "backend",
		IdentityFile: filepath.Join(dir, "custom-identity"),
		SelfName:     "prod",
		Name:         "node-a",
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Gateway.IdentityFile != filepath.Join(dir, "custom-identity") ||
		cfg.Gateway.SelfName != "prod" || cfg.Gateway.Name != "node-a" {
		t.Fatalf("explicit fields were overwritten: %+v", cfg.Gateway)
	}
}

func TestValidateInstance(t *testing.T) {
	for _, valid := range []string{"a", "backend-01", strings.Repeat("a", 32)} {
		if err := ValidateInstance(valid); err != nil {
			t.Fatalf("ValidateInstance(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Backend", "a/b", "a_b", strings.Repeat("a", 33)} {
		if err := ValidateInstance(invalid); err == nil {
			t.Fatalf("ValidateInstance(%q) accepted an invalid id", invalid)
		}
	}
}

func tempInstanceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestDefaultInstanceIDIsStableAndWorkspaceScoped(t *testing.T) {
	first, err := DefaultInstanceID("C:/work/app")
	if err != nil {
		t.Fatalf("DefaultInstanceID: %v", err)
	}
	again, err := DefaultInstanceID("C:/work/app")
	if err != nil || first != again {
		t.Fatalf("DefaultInstanceID changed: %q -> %q (%v)", first, again, err)
	}
	other, err := DefaultInstanceID("C:/work/other")
	if err != nil {
		t.Fatalf("DefaultInstanceID other: %v", err)
	}
	if first == other {
		t.Fatalf("different workspaces produced the same instance %q", first)
	}
	if !strings.HasPrefix(first, "auto-") {
		t.Fatalf("instance %q lacks the auto- prefix", first)
	}
}

func TestAutoInstanceClaimsLegacyIdentityOnce(t *testing.T) {
	home := tempInstanceHome(t)
	legacy := filepath.Join(home, ".aicli", "data", "gateway-agent", "mount-id")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("legacy-identity\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	first := GatewayConfig{URL: "wss://gw", AutoInstance: true}
	if err := first.resolveAutoInstance("C:/work/first"); err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	if !first.LegacyIdentity || first.Instance != "" || first.InstanceAuto {
		t.Fatalf("first workspace did not claim legacy: %+v", first)
	}

	second := GatewayConfig{URL: "wss://gw", AutoInstance: true}
	if err := second.resolveAutoInstance("C:/work/second"); err != nil {
		t.Fatalf("resolve second: %v", err)
	}
	if !second.InstanceAuto || second.LegacyIdentity || second.Instance == "" {
		t.Fatalf("second workspace did not get an auto instance: %+v", second)
	}
}

func TestNormalizeAutoInstanceDerivesIdentityAndNames(t *testing.T) {
	home := tempInstanceHome(t)
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Gateway = GatewayConfig{URL: "wss://gw", AutoInstance: true}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want, err := DefaultInstanceID(cfg.Workspace)
	if err != nil {
		t.Fatalf("DefaultInstanceID: %v", err)
	}
	if cfg.Gateway.Instance != want || !cfg.Gateway.InstanceAuto {
		t.Fatalf("instance = %q auto=%v, want %q", cfg.Gateway.Instance, cfg.Gateway.InstanceAuto, want)
	}
	wantIdentity := filepath.Join(home, ".aicli", "data", "gateway-agent", "instances", want, "mount-id")
	if cfg.Gateway.IdentityFile != wantIdentity {
		t.Fatalf("identity = %q, want %q", cfg.Gateway.IdentityFile, wantIdentity)
	}
	if cfg.Gateway.SelfName != want || cfg.Gateway.Name != want {
		t.Fatalf("labels = selfName %q, name %q", cfg.Gateway.SelfName, cfg.Gateway.Name)
	}
}

func TestEnsureInstanceBindingRejectsAnotherWorkspace(t *testing.T) {
	home := tempInstanceHome(t)
	id, err := DefaultInstanceID("C:/work/a")
	if err != nil {
		t.Fatalf("DefaultInstanceID: %v", err)
	}
	cfg := GatewayConfig{
		Instance:     id,
		InstanceAuto: true,
		IdentityFile: filepath.Join(home, ".aicli", "data", "gateway-agent", "instances", id, "mount-id"),
	}
	if err := ensureInstanceBinding("C:/work/a", cfg); err != nil {
		t.Fatalf("first binding: %v", err)
	}
	if err := ensureInstanceBinding("C:/work/a", cfg); err != nil {
		t.Fatalf("same workspace binding: %v", err)
	}
	err = ensureInstanceBinding("C:/work/b", cfg)
	if err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("duplicate binding error = %v, want an --instance hint", err)
	}
}

func TestDefaultInstanceIDIncludesMachine(t *testing.T) {
	first := DefaultInstanceIDForBinding(instanceBinding{Machine: "machine-a", Workspace: "/home"})
	second := DefaultInstanceIDForBinding(instanceBinding{Machine: "machine-b", Workspace: "/home"})
	if first == second {
		t.Fatalf("two machines with the same /home produced the same instance %q", first)
	}
	if !strings.HasPrefix(first, "auto-") {
		t.Fatalf("instance %q lacks the auto- prefix", first)
	}
}

func TestLegacyMarkerAdoptsPlainWorkspaceAndDistinguishesMachines(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "legacy-workspace")
	if err := os.WriteFile(marker, []byte("/home\n"), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
	current := instanceBinding{Machine: "machine-a", Workspace: "/home"}
	claimed, err := claimLegacyWorkspace(marker, current)
	if err != nil || !claimed {
		t.Fatalf("plain marker claim = %v, %v; want true", claimed, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	parsed, structured := parseInstanceBinding(data)
	if !structured || parsed != current {
		t.Fatalf("marker = %+v structured=%v, want %+v", parsed, structured, current)
	}

	other := instanceBinding{Machine: "machine-b", Workspace: "/home"}
	claimed, err = claimLegacyWorkspace(marker, other)
	if err != nil || claimed {
		t.Fatalf("second machine claim = %v, %v; want false", claimed, err)
	}
}

func TestEnsureInstanceBindingRejectsAnotherMachine(t *testing.T) {
	workspace := t.TempDir()
	key, err := workspaceInstanceKey(workspace)
	if err != nil {
		t.Fatalf("workspaceInstanceKey: %v", err)
	}
	id := "auto-machine-check"
	dir := filepath.Join(t.TempDir(), "instances", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(dir, "workspace")
	body := fmt.Sprintf(`{"machine":"definitely-not-this-machine","workspace":%q}`+"\n", key)
	if err := os.WriteFile(marker, []byte(body), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	cfg := GatewayConfig{
		Instance:     id,
		InstanceAuto: true,
		IdentityFile: filepath.Join(dir, "mount-id"),
	}
	err = ensureInstanceBinding(workspace, cfg)
	if err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("machine mismatch error = %v, want an --instance hint", err)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestBuildMountRegistrationReadsTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "mcp-token")
	if err := os.WriteFile(tokenPath, []byte("file-secret\r\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Setenv("SHOULD_NOT_BE_USED", "env-secret")

	cfg := DefaultConfig()
	cfg.Workspace = dir
	cfg.HTTP.Path = "/mcp"
	cfg.HTTP.TokenFile = tokenPath
	cfg.HTTP.TokenEnv = "SHOULD_NOT_BE_USED"
	cfg.Gateway = GatewayConfig{URL: "wss://gw", MountSelf: boolPtr(true)}

	registration, err := BuildMountRegistration(cfg, "identity-1", "127.0.0.1:18765")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	forward := registration.Forwards[0]
	if forward.TokenValue != "file-secret" {
		t.Fatalf("forward token = %q, want file-secret (file must beat env)", forward.TokenValue)
	}
	if forward.TokenEnv != "SHOULD_NOT_BE_USED" {
		t.Fatalf("wire token env = %q (name is still reported for audit)", forward.TokenEnv)
	}
}

func TestEnableGatewayResolvesTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "gateway-token")
	if err := os.WriteFile(tokenPath, []byte("dev-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	server := &Server{cfg: &Config{Transport: "http"}}
	err := server.EnableGateway(context.Background(), GatewayConfig{
		URL:          "wss://gw.example.com",
		TokenFile:    tokenPath,
		IdentityFile: filepath.Join(dir, "identity"),
	})
	if err != nil {
		t.Fatalf("EnableGateway: %v", err)
	}
	if server.gatewayToken != "dev-token" {
		t.Fatalf("gateway token = %q", server.gatewayToken)
	}
}

func TestEnableGatewayFailsClosedWithoutToken(t *testing.T) {
	t.Setenv(DefaultGatewayTokenEnv, "")
	server := &Server{cfg: &Config{Transport: "http"}}
	err := server.EnableGateway(context.Background(), GatewayConfig{
		URL:          "wss://gw.example.com",
		IdentityFile: filepath.Join(t.TempDir(), "identity"),
	})
	if err == nil || !strings.Contains(err.Error(), "token is empty") {
		t.Fatalf("missing token error = %v", err)
	}
}
func TestEnableGatewayReusesAICLIConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `gateway:
  url: https://aicli.ttai.online
  token: agw_from_tui
  name: laptop
  proxy_url: http://127.0.0.1:10810
  auto_connect: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write aicli config: %v", err)
	}
	t.Setenv(DefaultGatewayTokenEnv, "")

	enabled := true
	server := &Server{cfg: &Config{Transport: "http"}}
	err := server.EnableGateway(context.Background(), GatewayConfig{
		UseAICLIConfig:  &enabled,
		AICLIConfigFile: path,
		IdentityFile:    filepath.Join(dir, "identity"),
	})
	if err != nil {
		t.Fatalf("EnableGateway: %v", err)
	}
	if server.gatewayCfg.URL != "https://aicli.ttai.online" {
		t.Fatalf("url = %q", server.gatewayCfg.URL)
	}
	if server.gatewayCfg.ProxyURL != "http://127.0.0.1:10810" {
		t.Fatalf("proxy = %q", server.gatewayCfg.ProxyURL)
	}
	if server.gatewayCfg.Name != "laptop" {
		t.Fatalf("name = %q", server.gatewayCfg.Name)
	}
	if server.gatewayToken != "agw_from_tui" {
		t.Fatalf("token = %q, want agw_from_tui", server.gatewayToken)
	}
}

func TestEnableGatewayTokenFileBeatsAICLIConfig(t *testing.T) {
	dir := t.TempDir()
	aicliPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(aicliPath, []byte("gateway:\n  url: https://gw\n  token: agw_from_tui\n"), 0o600); err != nil {
		t.Fatalf("write aicli config: %v", err)
	}
	tokenPath := filepath.Join(dir, "gateway.token")
	if err := os.WriteFile(tokenPath, []byte("agw_from_file\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv(DefaultGatewayTokenEnv, "")

	enabled := true
	server := &Server{cfg: &Config{Transport: "http"}}
	err := server.EnableGateway(context.Background(), GatewayConfig{
		UseAICLIConfig:  &enabled,
		AICLIConfigFile: aicliPath,
		TokenFile:       tokenPath,
		IdentityFile:    filepath.Join(dir, "identity"),
	})
	if err != nil {
		t.Fatalf("EnableGateway: %v", err)
	}
	if server.gatewayToken != "agw_from_file" {
		t.Fatalf("token = %q, want the explicit token file to win", server.gatewayToken)
	}
}
