package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workspace-scoped chat preference persistence (decision D5).
//
// The global aicli.chat section is shared across every working directory, so a
// preference saved in one workspace silently changes the startup defaults of
// another — the interactive provider/model pickers then fire in workspaces the
// user already configured. The workspace layer stores the same
// AICLIChatPreferenceUpdate shape under
// $HOME/.aicli/workspace/<hash>/chat-prefs.yaml where <hash> is derived from
// the current working directory (see HeaderTemplateProjectID's hashing), so
// each workspace remembers its own provider/model/reasoning/stream defaults
// and the interactive startup selectors stop firing once saved.
//
// Precedence (per key): CLI flag > restored session context > workspace
// preference > global aicli.chat > interactive prompt > provider default.

// workspacePrefsDirName is the directory under $HOME/.aicli holding the
// per-workspace preference folders.
const workspacePrefsDirName = "workspace"

// workspacePrefsFileName is the preference file inside a workspace folder.
const workspacePrefsFileName = "chat-prefs.yaml"

// workspaceCwd is swappable in tests, mirroring the userHomeDir pattern.
var workspaceCwd = os.Getwd

// WorkspaceCwdForTest returns the current workspace-cwd resolver.
func WorkspaceCwdForTest() func() (string, error) {
	return workspaceCwd
}

// SetWorkspaceCwdForTest replaces the workspace-cwd resolver used to derive
// the workspace preference identity. It is intended for tests only.
func SetWorkspaceCwdForTest(resolver func() (string, error)) {
	if resolver == nil {
		workspaceCwd = os.Getwd
		return
	}
	workspaceCwd = resolver
}

func WorkspacePrefsID() string {
	cwd, err := workspaceCwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return ""
	}
	return WorkspacePrefsIDForPath(cwd)
}

// WorkspacePrefsIDForPath derives the workspace identifier for an explicit
// path, using the same hashing as WorkspacePrefsID.
func WorkspacePrefsIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return WorkspacePrefsIDForCleanedPath(filepath.Clean(path))
}

// WorkspacePrefsIDForCleanedPath is the test seam taking an already-cleaned
// path. Tests pass a fake cwd through SetWorkspaceCwdForTest.
func WorkspacePrefsIDForCleanedPath(cleanedPath string) string {
	if strings.TrimSpace(cleanedPath) == "" {
		return ""
	}
	return projectIDForPath(cleanedPath)
}

// WorkspacePrefsPath returns the preference file path for the current working
// directory, or "" when the cwd/home cannot be resolved.
func WorkspacePrefsPath() string {
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	id := WorkspacePrefsID()
	if id == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", workspacePrefsDirName, id, workspacePrefsFileName)
}

// LoadWorkspaceChatPreferences reads the workspace-scoped chat preferences for
// the current working directory. A missing file yields an empty struct and no
// error; parse/read failures are reported so callers can warn.
func LoadWorkspaceChatPreferences() (*AICLIChatConfig, error) {
	path := WorkspacePrefsPath()
	if path == "" {
		return nil, nil
	}
	return loadWorkspaceChatPreferencesAt(path)
}

func loadWorkspaceChatPreferencesAt(path string) (*AICLIChatConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace chat preferences %s: %w", path, err)
	}
	current, err := currentAICLIChatConfigFromYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("parse workspace chat preferences %s: %w", path, err)
	}
	return current, nil
}

// SaveWorkspaceChatPreferences merges the update into the workspace preference
// file for the current working directory, creating the directory and file on
// first write. It never touches the global config layers.
func SaveWorkspaceChatPreferences(update AICLIChatPreferenceUpdate) error {
	path := WorkspacePrefsPath()
	if path == "" {
		return fmt.Errorf("workspace chat preferences unavailable: cannot resolve cwd or home")
	}
	return saveWorkspaceChatPreferencesAt(path, update)
}

func saveWorkspaceChatPreferencesAt(path string, update AICLIChatPreferenceUpdate) error {
	current := &AICLIChatConfig{}
	if raw, err := os.ReadFile(path); err == nil {
		loaded, parseErr := currentAICLIChatConfigFromYAML(raw)
		if parseErr != nil {
			return fmt.Errorf("parse workspace chat preferences %s: %w", path, parseErr)
		}
		if loaded != nil {
			current = loaded
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read workspace chat preferences %s: %w", path, err)
	}

	applyAICLIChatPreferenceUpdate(current, update)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create workspace preferences directory: %w", err)
	}
	if err := writeWorkspacePrefsYAML(path, current); err != nil {
		return err
	}
	return nil
}

// ClearWorkspaceChatPreferences removes the workspace preference file for the
// current working directory. A missing file is not an error.
func ClearWorkspaceChatPreferences() error {
	path := WorkspacePrefsPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove workspace chat preferences %s: %w", path, err)
	}
	return nil
}

// projectIDForPath hashes an already-cleaned path the same way
// providerops.HeaderTemplateProjectID does (sha256, first 8 bytes, hex), so
// workspace preferences, session project identity and gateway routing all
// agree on "which workspace is this".
func projectIDForPath(cleanedPath string) string {
	if strings.TrimSpace(cleanedPath) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cleanedPath))
	return hex.EncodeToString(sum[:8])
}

// currentAICLIChatConfigFromYAML decodes an aicli.chat-shaped YAML document
// (same shape the global config's aicli.chat section uses).
func currentAICLIChatConfigFromYAML(raw []byte) (*AICLIChatConfig, error) {
	current := &AICLIChatConfig{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return current, nil
	}
	// The workspace file stores the chat section at the top level, but accept a
	// full-document shape (aicli.chat nested) as well so hand-edited files and
	// copies from the global config both work.
	probe := &struct {
		AICLI *struct {
			Chat *AICLIChatConfig `yaml:"chat"`
		} `yaml:"aicli"`
	}{}
	if err := yaml.Unmarshal(raw, probe); err != nil {
		return nil, fmt.Errorf("decode workspace chat preferences: %w", err)
	}
	if probe.AICLI != nil && probe.AICLI.Chat != nil {
		return probe.AICLI.Chat, nil
	}
	if err := yaml.Unmarshal(raw, current); err != nil {
		return nil, fmt.Errorf("decode workspace chat preferences: %w", err)
	}
	return current, nil
}

// writeWorkspacePrefsYAML encodes the preferences as an aicli.chat-shaped
// document (nested under aicli.chat) and writes it atomically.
func writeWorkspacePrefsYAML(path string, prefs *AICLIChatConfig) error {
	document := map[string]interface{}{
		"aicli": map[string]interface{}{
			"chat": prefs,
		},
	}
	out, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode workspace chat preferences: %w", err)
	}
	return writeFileAtomic(path, out)
}
