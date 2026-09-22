package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func isolateConfigLayerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := UserHomeDirForTest()
	SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { SetUserHomeDirForTest(previous) })
	return home
}

func writeConfigLayerFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config layer dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config layer %s: %v", path, err)
	}
}

func TestParseMergeModeNormalisesValues(t *testing.T) {
	cases := map[string]MergeMode{
		"":        MergeModeOff,
		"off":     MergeModeOff,
		"false":   MergeModeOff,
		"bogus":   MergeModeOff,
		"dry-run": MergeModeDryRun,
		"preview": MergeModeDryRun,
		"dryrun":  MergeModeDryRun,
		"on":      MergeModeOn,
		" TRUE ":  MergeModeOn,
		"enabled": MergeModeOn,
	}
	for raw, want := range cases {
		if got := ParseMergeMode(raw); got != want {
			t.Fatalf("ParseMergeMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The layer stack is the single source of truth for precedence; both aicli and
// runtime-server derive their candidate list from it. This pins the low-to-high
// ordering and the high-to-low search order derived from it.
func TestConfigLayerStackOrderMatchesSearchPaths(t *testing.T) {
	home := isolateConfigLayerHome(t)
	names := defaultConfigSearchNames()

	expectedKinds := make([]string, 0, 4*len(names)+1)
	for range names {
		expectedKinds = append(expectedKinds, string(LayerKindPortable))
	}
	for range names {
		expectedKinds = append(expectedKinds, string(LayerKindLegacy))
	}
	expectedKinds = append(expectedKinds, string(LayerKindLegacy))
	for range names {
		expectedKinds = append(expectedKinds, string(LayerKindUser))
	}
	for range names {
		expectedKinds = append(expectedKinds, string(LayerKindProject))
	}

	stack := ConfigLayerStack()
	kinds := make([]string, 0, len(stack))
	for _, layer := range stack {
		kinds = append(kinds, string(layer.Kind))
	}
	if strings.Join(kinds, ",") != strings.Join(expectedKinds, ",") {
		t.Fatalf("layer kinds = %v, want %v", kinds, expectedKinds)
	}

	// The user layer must be the current home's user config.
	userLayer := stack[len(stack)-1-len(names)]
	if userLayer.Kind != LayerKindUser {
		t.Fatalf("expected user layer, got %q", userLayer.Kind)
	}
	if want := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName); userLayer.Path != want {
		t.Fatalf("user layer path = %q, want %q", userLayer.Path, want)
	}

	// The search order must be exactly the reverse of the layer stack.
	searchPaths := ConfigLayerSearchPaths()
	if len(searchPaths) != len(stack) {
		t.Fatalf("search paths = %v, want %d entries", searchPaths, len(stack))
	}
	for index, path := range searchPaths {
		if want := stack[len(stack)-1-index].Path; path != want {
			t.Fatalf("search path %d = %q, want %q", index, path, want)
		}
	}
	if got := DefaultConfigSearchPaths(); strings.Join(got, "\n") != strings.Join(searchPaths, "\n") {
		t.Fatalf("DefaultConfigSearchPaths drifted from the shared layer stack:\n got: %v\nwant: %v", got, searchPaths)
	}
}

func TestLayeredConfigDocumentMergesDeeplyMasksNullsAndReplacesSlices(t *testing.T) {
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), `
custom_list:
  - user-a
  - user-b
nested:
  keep: user
  drop: user
empty_map:
  keep: user
`)
	projectDir := t.TempDir()
	writeConfigLayerFile(t, filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName), `
custom_list:
  - project-only
nested:
  drop:
empty_map: {}
`)
	t.Chdir(projectDir)

	document, err := loadLayeredConfigDocument()
	if err != nil {
		t.Fatalf("loadLayeredConfigDocument: %v", err)
	}

	list, ok := document.Merged["custom_list"].([]interface{})
	if !ok || len(list) != 1 || list[0] != "project-only" {
		t.Fatalf("slices must be replaced, not appended: %#v", document.Merged["custom_list"])
	}

	nested, ok := document.Merged["nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("nested map missing: %#v", document.Merged["nested"])
	}
	if nested["keep"] != "user" {
		t.Fatalf("keys the project layer omits must fall back to the user layer: %#v", nested)
	}
	if _, exists := nested["drop"]; exists {
		t.Fatalf("an explicit null must mask the lower layer value: %#v", nested)
	}
	if _, exists := document.Origins["nested.drop"]; exists {
		t.Fatalf("masked keys must not carry an origin: %#v", document.Origins)
	}
	if got := document.Origins["nested.keep"]; got != string(LayerKindUser) {
		t.Fatalf("origin of nested.keep = %q, want %q", got, LayerKindUser)
	}
	if got := document.Origins["custom_list"]; got != string(LayerKindProject) {
		t.Fatalf("origin of custom_list = %q, want %q", got, LayerKindProject)
	}

	emptyMap, ok := document.Merged["empty_map"].(map[string]interface{})
	if !ok || emptyMap["keep"] != "user" {
		t.Fatalf("an empty map must not clear lower layers (it means \"no keys written\"): %#v", document.Merged["empty_map"])
	}

	// The project layer wins for the source path, and the user-owned key is
	// reported as a fallback key because single-file mode would not see it.
	if want := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName); document.SourcePath != want {
		t.Fatalf("source path = %q, want %q", document.SourcePath, want)
	}
	found := false
	for _, key := range document.FallbackKeys {
		if key == "nested.keep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nested.keep must be reported as a fallback key: %v", document.FallbackKeys)
	}
}

func TestLayeredLoadFallsBackToUserKeysAndReportsOrigins(t *testing.T) {
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  default_provider: user-provider
  headers:
    X-User: "1"
`)
	projectDir := t.TempDir()
	writeConfigLayerFile(t, filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  headers:
    X-Project: "1"
`)
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}

	// The project layer did not write default_provider, so the user value must
	// survive: this is the whole point of layered merging.
	if got := cfg.Providers.DefaultProvider; got != "user-provider" {
		t.Fatalf("default provider = %q, want user-provider (fallback from the user layer)", got)
	}
	if got := cfg.Providers.Headers["X-User"]; got != "1" {
		t.Fatalf("user header lost: %#v", cfg.Providers.Headers)
	}
	if got := cfg.Providers.Headers["X-Project"]; got != "1" {
		t.Fatalf("project header lost: %#v", cfg.Providers.Headers)
	}
	if got := cfg.ConfigMergeMode; got != MergeModeOn {
		t.Fatalf("merge mode = %q, want %q", got, MergeModeOn)
	}
	if want := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName); cfg.ConfigFilePath != want {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, want)
	}
	if got := cfg.ConfigOrigins["providers.default_provider"]; got != string(LayerKindUser) {
		t.Fatalf("origin of providers.default_provider = %q, want %q", got, LayerKindUser)
	}
	if got := cfg.ConfigOrigins["providers.headers.X-Project"]; got != string(LayerKindProject) {
		t.Fatalf("origin of providers.headers.X-Project = %q, want %q", got, LayerKindProject)
	}
	if len(cfg.ConfigLayers) == 0 {
		t.Fatal("expected the candidate stack to be recorded for diagnostics")
	}
}

func TestMergeModeOffIgnoresLowerLayers(t *testing.T) {
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  default_provider: user-provider
`)
	projectDir := t.TempDir()
	writeConfigLayerFile(t, filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  headers:
    X-Project: "1"
`)
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "off")

	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if got := cfg.Providers.DefaultProvider; got != "" {
		t.Fatalf("off mode must keep single-file semantics, got default provider %q", got)
	}
	if got := cfg.ConfigMergeMode; got != MergeModeOff {
		t.Fatalf("merge mode = %q, want %q", got, MergeModeOff)
	}
	if len(cfg.ConfigLayers) == 0 {
		t.Fatal("off mode must still record the candidate stack for diagnostics")
	}
}

func TestMergeModeDryRunKeepsSingleFileBehaviour(t *testing.T) {
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  default_provider: user-provider
`)
	projectDir := t.TempDir()
	writeConfigLayerFile(t, filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  headers:
    X-Project: "1"
`)
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "dry-run")

	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if got := cfg.Providers.DefaultProvider; got != "" {
		t.Fatalf("dry-run must not change behaviour, got default provider %q", got)
	}
	if got := cfg.ConfigMergeMode; got != MergeModeDryRun {
		t.Fatalf("merge mode = %q, want %q", got, MergeModeDryRun)
	}
}

func TestExplicitConfigPathShortCircuitsLayering(t *testing.T) {
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  default_provider: user-provider
`)
	projectDir := t.TempDir()
	writeConfigLayerFile(t, filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName), `
providers:
  headers:
    X-Project: "1"
`)
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	explicitPath := filepath.Join(t.TempDir(), "picked.yaml")
	writeConfigLayerFile(t, explicitPath, `
providers:
  default_provider: explicit-provider
`)

	cfg, err := InitGlobalConfigLayered(explicitPath, explicitPath)
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if got := cfg.Providers.DefaultProvider; got != "explicit-provider" {
		t.Fatalf("default provider = %q, want explicit-provider", got)
	}
	if _, exists := cfg.Providers.Headers["X-Project"]; exists {
		t.Fatalf("an explicit --config selection must not merge other layers: %#v", cfg.Providers.Headers)
	}
	if want := filepath.Clean(explicitPath); cfg.ConfigFilePath != want {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, want)
	}
}
