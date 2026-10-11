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
// Inheritance: reads walk the workspace's ancestor directories (from the
// filesystem root down to the workspace) and deep-merge every existing
// chat-prefs.yaml, nearest wins per key (see
// WorkspacePrefsInheritanceChainPaths). A subdirectory therefore inherits its
// parents' preferences and only has to persist the keys it overrides, while a
// write always targets the workspace's own file — an inherited value is never
// materialized into the child, so clearing the child falls back to the parent.
//
// Precedence (per key): CLI flag > restored session context > workspace
// preference (including inherited ancestors) > global aicli.chat > interactive
// prompt > provider default.

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
	cwd, err := workspaceCwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return ""
	}
	return WorkspacePrefsPathForPath(cwd)
}

// WorkspacePrefsPathForPath derives the preference file path for an explicit
// workspace path. 方案 §3.3/N9：读写以会话绑定 workspace 路径为准，而不是
// 当前 cwd——否则 /resume 到其他目录的会话会静默改错文件。
func WorkspacePrefsPathForPath(path string) string {
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	id := WorkspacePrefsIDForPath(path)
	if id == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", workspacePrefsDirName, id, workspacePrefsFileName)
}

// LoadWorkspaceChatPreferences reads the effective workspace-scoped chat
// preferences for the current working directory. Preferences are inherited
// from ancestor directories (see WorkspacePrefsInheritanceChainPaths): the
// nearest directory that defines a key wins, so a subdirectory only needs to
// store the keys it overrides and inherits everything else from its parents.
// A missing file yields (nil, nil); parse/read failures are reported so
// callers can warn.
func LoadWorkspaceChatPreferences() (*AICLIChatConfig, error) {
	cwd, err := workspaceCwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return nil, nil
	}
	return loadWorkspaceChatPreferencesMerged(cwd)
}

// LoadWorkspaceChatPreferencesForPath is the explicit-path variant of
// LoadWorkspaceChatPreferences (方案 §3.3/N9：以会话绑定 workspace 路径为准)，
// 同样包含父目录继承。
func LoadWorkspaceChatPreferencesForPath(workspacePath string) (*AICLIChatConfig, error) {
	return loadWorkspaceChatPreferencesMerged(workspacePath)
}

// loadWorkspaceChatPreferencesAt reads exactly one preference file, with no
// inheritance. Writers use it to merge a patch into the file they own, so an
// update never materializes inherited ancestor values into the child file.
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

// WorkspacePrefsInheritanceChainPaths returns the preference file paths to
// merge for workspacePath, ordered from the outermost ancestor down to the
// workspace itself. A subdirectory therefore inherits its parents' preferences
// while its own file overrides individual keys (nearer wins). The slice is
// empty when the path or the user home cannot be resolved.
func WorkspacePrefsInheritanceChainPaths(workspacePath string) []string {
	cleaned := strings.TrimSpace(workspacePath)
	if cleaned == "" {
		return nil
	}
	cleaned = filepath.Clean(cleaned)
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}

	// Collect the directory and every ancestor up to the filesystem root.
	dirs := make([]string, 0, 8)
	for dir := cleaned; ; {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Reverse so the outermost ancestor comes first: merging in this order lets
	// the workspace's own file override its parents key by key.
	paths := make([]string, 0, len(dirs))
	for i := len(dirs) - 1; i >= 0; i-- {
		id := WorkspacePrefsIDForCleanedPath(dirs[i])
		if id == "" {
			continue
		}
		paths = append(paths, filepath.Join(home, ".aicli", workspacePrefsDirName, id, workspacePrefsFileName))
	}
	return paths
}

// loadWorkspaceChatPreferencesMerged resolves the inheritance chain for
// workspacePath and deep-merges every existing ancestor preference document
// (root first, nearest last) so nearer directories override farther ones.
func loadWorkspaceChatPreferencesMerged(workspacePath string) (*AICLIChatConfig, error) {
	chain := WorkspacePrefsInheritanceChainPaths(workspacePath)
	if len(chain) == 0 {
		return nil, nil
	}
	merged := map[string]interface{}{}
	found := false
	for _, path := range chain {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read workspace chat preferences %s: %w", path, err)
		}
		section, err := chatSectionMapFromYAML(raw)
		if err != nil {
			return nil, fmt.Errorf("parse workspace chat preferences %s: %w", path, err)
		}
		if len(section) == 0 {
			continue
		}
		mergeStringMap(merged, section)
		found = true
	}
	if !found {
		return nil, nil
	}
	raw, err := yaml.Marshal(map[string]interface{}{
		"aicli": map[string]interface{}{"chat": merged},
	})
	if err != nil {
		return nil, fmt.Errorf("encode merged workspace chat preferences: %w", err)
	}
	return currentAICLIChatConfigFromYAML(raw)
}

// chatSectionMapFromYAML decodes an aicli.chat-shaped YAML document into a
// plain map, accepting both the top-level chat shape written by
// writeWorkspacePrefsYAML and the nested aicli.chat shape, mirroring
// currentAICLIChatConfigFromYAML.
func chatSectionMapFromYAML(raw []byte) (map[string]interface{}, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode workspace chat preferences: %w", err)
	}
	if doc == nil {
		return nil, nil
	}
	if aicli, ok := doc["aicli"].(map[string]interface{}); ok {
		chat, _ := aicli["chat"].(map[string]interface{})
		return chat, nil
	}
	return doc, nil
}

// mergeStringMap deep-merges src into dst: nested mappings merge recursively
// and every scalar/sequence value from src wins, so a nearer (child) document
// overrides farther (parent) documents key by key.
func mergeStringMap(dst, src map[string]interface{}) {
	for key, srcValue := range src {
		if srcMap, ok := srcValue.(map[string]interface{}); ok {
			if dstMap, ok := dst[key].(map[string]interface{}); ok {
				mergeStringMap(dstMap, srcMap)
				continue
			}
		}
		dst[key] = srcValue
	}
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

// SaveWorkspaceChatPreferencesForPath 是 SaveWorkspaceChatPreferences 的显式路径
// 版本（方案 §5.4/N9）：路由面板与 runtime API 以**会话绑定 workspace 路径**写入，
// 不随当前 shell 的 cwd 漂移。
func SaveWorkspaceChatPreferencesForPath(workspacePath string, update AICLIChatPreferenceUpdate) error {
	path := WorkspacePrefsPathForPath(workspacePath)
	if path == "" {
		return fmt.Errorf("workspace chat preferences unavailable: cannot resolve path %q", strings.TrimSpace(workspacePath))
	}
	return saveWorkspaceChatPreferencesAt(path, update)
}

func saveWorkspaceChatPreferencesAt(path string, update AICLIChatPreferenceUpdate) error {
	unlock := LockConfigFileWrite(path)
	defer unlock()
	return saveWorkspaceChatPreferencesAtLocked(path, update)
}

// saveWorkspaceChatPreferencesAtLocked 假定调用方已持有 path 的写锁：工作区偏好
// 的写入是「读-改-写」，UpdateWorkspaceRoutingSection 需要先读旧值再合并补丁，
// 整段在同一把锁内完成，落盘因此不能再次取锁（Go 互斥锁不重入）。
func saveWorkspaceChatPreferencesAtLocked(path string, update AICLIChatPreferenceUpdate) error {
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
	// 删除也是同一份文件的变更：与写者共用同一把锁，避免「删除」与「读-改-写」
	// 交错（写者基于已删除的旧文件落盘，或刚写入的内容被删除命令吃掉）。
	unlock := LockConfigFileWrite(path)
	defer unlock()
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
