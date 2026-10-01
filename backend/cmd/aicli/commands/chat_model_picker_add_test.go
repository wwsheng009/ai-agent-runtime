package commands

import (
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// chatModelAddTestConfig mirrors chatModelDeleteTestConfig: the provider node
// is created through the production persistence path so the assertions on the
// on-disk file exercise the same writer the picker uses.
func chatModelAddTestConfig(t *testing.T, path, providerName, defaultModel string, models []string) *config.Config {
	t.Helper()
	return chatModelDeleteTestConfig(t, path, providerName, defaultModel, models)
}
func TestSplitChatModelAdditionsSplitsPasteAndReportsDuplicates(t *testing.T) {
	provider := config.Provider{
		DefaultModel:    "m-a",
		SupportedModels: []string{"m-a", "m-b"},
	}
	add, skipped := splitChatModelAdditions("  m-c  m-d,m-b  m-c\nm-E ", provider)
	if got, want := strings.Join(add, ","), "m-c,m-d,m-E"; got != want {
		t.Fatalf("add = %q, want %q", got, want)
	}
	// m-b already present, m-c pasted twice: reported once, never added twice.
	if got, want := strings.Join(skipped, ","), "m-b"; got != want {
		t.Fatalf("skipped = %q, want %q", got, want)
	}
}

func TestSplitChatModelAdditionsTreatsDefaultModelAsPresent(t *testing.T) {
	// The provider default is not in supported_models, but re-adding it would
	// duplicate the model in the picker, so it must count as present.
	provider := config.Provider{DefaultModel: "m-default"}
	add, skipped := splitChatModelAdditions("m-default m-new", provider)
	if len(add) != 1 || add[0] != "m-new" {
		t.Fatalf("add = %v, want [m-new]", add)
	}
	if len(skipped) != 1 || skipped[0] != "m-default" {
		t.Fatalf("skipped = %v, want [m-default]", skipped)
	}
}

func TestChatModelAdditionTextError(t *testing.T) {
	provider := config.Provider{SupportedModels: []string{"m-a", "m-b"}}
	cases := []struct {
		name    string
		text    string
		wantErr string
	}{
		{name: "empty", text: "   ", wantErr: "请输入"},
		{name: "only whitespace and commas", text: " , , ", wantErr: "请输入"},
		{name: "all duplicates", text: "m-a m-b", wantErr: "已存在"},
		{name: "case-insensitive duplicate", text: "M-A", wantErr: "已存在"},
		{name: "one new", text: "m-c"},
		// Partial duplicates are accepted: refusing the whole paste over one
		// duplicate is hostile when the user is pasting a long catalog.
		{name: "partial duplicate", text: "m-a m-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := chatModelAdditionTextError(provider, tc.text)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestPersistChatModelAdditionAppendsAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})

	if err := persistChatModelAddition(cfg, "alpha", []string{"m-c", "m-d"}); err != nil {
		t.Fatalf("persistChatModelAddition: %v", err)
	}
	got := readProviderModelsInConfig(t, path, "alpha")
	if want := []string{"m-a", "m-b", "m-c", "m-d"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("persisted models = %v, want %v", got, want)
	}
	// In-memory copy mirrors the file so the reopened stage lists the new model.
	inMemory := cfg.Providers.Items["alpha"].SupportedModels
	if want := []string{"m-a", "m-b", "m-c", "m-d"}; strings.Join(inMemory, ",") != strings.Join(want, ",") {
		t.Fatalf("in-memory models = %v, want %v", inMemory, want)
	}
	// Adding must not silently change which model is active.
	if cfg.Providers.Items["alpha"].DefaultModel != "m-a" {
		t.Fatalf("default model changed to %q", cfg.Providers.Items["alpha"].DefaultModel)
	}
}

func TestPersistChatModelAdditionIsIdempotentAndCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})

	// Same id twice in one call, and a case variant of an existing entry.
	if err := persistChatModelAddition(cfg, "alpha", []string{"m-c", "m-c", "M-B", "m-d"}); err != nil {
		t.Fatalf("persistChatModelAddition: %v", err)
	}
	got := readProviderModelsInConfig(t, path, "alpha")
	if want := []string{"m-a", "m-b", "m-c", "m-d"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("persisted models = %v, want %v (duplicates/case variants must be dropped)", got, want)
	}
}

func TestPersistChatModelAdditionRejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a"})

	if err := persistChatModelAddition(cfg, "nope", []string{"m-c"}); err == nil ||
		!strings.Contains(err.Error(), "不存在") {
		t.Fatalf("err = %v, want provider-not-found", err)
	}
	// Nothing was written.
	if got := readProviderModelsInConfig(t, path, "alpha"); len(got) != 1 || got[0] != "m-a" {
		t.Fatalf("alpha models changed: %v", got)
	}
}

func TestPersistChatModelAdditionRejectsAllDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})

	if err := persistChatModelAddition(cfg, "alpha", []string{"m-a"}); err == nil ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("err = %v, want already-exists", err)
	}
	if got := readProviderModelsInConfig(t, path, "alpha"); len(got) != 2 {
		t.Fatalf("models changed on rejected add: %v", got)
	}
}

// Add and remove must compose: the exact round trip a user does when they add a
// model by mistake and take it back.
func TestPersistChatModelAddThenRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := chatModelAddTestConfig(t, path, "alpha", "m-a", []string{"m-a", "m-b"})

	if err := persistChatModelAddition(cfg, "alpha", []string{"m-c"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := persistChatModelRemoval(cfg, "alpha", "m-c"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readProviderModelsInConfig(t, path, "alpha")
	if want := []string{"m-a", "m-b"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after round trip = %v, want %v", got, want)
	}
}
