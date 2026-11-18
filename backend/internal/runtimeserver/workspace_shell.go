package runtimeserver

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"gopkg.in/yaml.v3"
)

// WorkspaceTrustedForShell resolves the server-side folder-trust decision for
// workspace-owned shell configuration. runtime-server never prompts; an
// undecided workspace is untrusted (fail closed).
func WorkspaceTrustedForShell(workspacePath string) bool {
	path := strings.TrimSpace(workspacePath)
	if path == "" {
		return true
	}
	interactive := false
	return foldertrust.Resolve(foldertrust.ResolveOptions{
		CWD:         path,
		Interactive: &interactive,
		SkipPrompt:  true,
	}).Trusted
}

// WorkspaceShellResolver resolves the shell execution environment for one
// session workspace.
//
// runtime-server is a long-lived multi-workspace process: the process-wide
// resolver installed at startup is only a fallback. Each session must carry the
// runtime.yaml stack anchored at its own workspace, otherwise workspace A's
// shell leaks into workspace B (and concurrent requests can mutate a shared
// global singleton).
//
// Precedence:
//
//	profile runtime.yaml > explicit skills_runtime.config_file > workspace .aicli > user .aicli > global fallback
type WorkspaceShellResolver struct {
	mu         sync.Mutex
	cache      map[string]*workspaceShellEntry
	maxEntries int
	global     *runtimeexecutor.ShellRuntime
	globalPath string
	explicit   string
	trust      func(workspacePath string) bool
}

type workspaceShellEntry struct {
	fingerprint string
	runtime     *runtimeexecutor.ShellRuntime
	sourcePath  string
	lastUse     time.Time
}

// NewWorkspaceShellResolver builds a resolver.
//
// global is the process-wide fallback (usually resolved from the user-level
// runtime.yaml). explicit is the non-convention skills_runtime.config_file
// override, if any. trust gates project-level shell.path/shell.env from
// workspace-owned files; nil means trusted (CLI/tests).
func NewWorkspaceShellResolver(
	global *runtimeexecutor.ShellRuntime,
	globalPath string,
	explicit string,
	trust func(workspacePath string) bool,
) *WorkspaceShellResolver {
	return &WorkspaceShellResolver{
		cache:      make(map[string]*workspaceShellEntry),
		maxEntries: 16,
		global:     global,
		globalPath: strings.TrimSpace(globalPath),
		explicit:   strings.TrimSpace(explicit),
		trust:      trust,
	}
}

// Resolve returns the shell runtime for workspacePath.
//
// profileRuntimePath is the runtime.yaml selected by a session profile, when
// one is explicitly declared. The returned sourcePath is the effective
// runtime.yaml (or the global source path) for diagnostics and settings.
func (r *WorkspaceShellResolver) Resolve(workspacePath, profileRuntimePath string) (*runtimeexecutor.ShellRuntime, string, error) {
	if r == nil {
		return r.fallback()
	}
	workspacePath = normalizeWorkspaceShellPath(workspacePath)
	profileRuntimePath = strings.TrimSpace(profileRuntimePath)
	trusted := true
	if workspacePath != "" && r.trust != nil {
		trusted = r.trust(workspacePath)
	}

	// A profile-declared runtime.yaml is an explicit per-session override. If it
	// lives inside an untrusted workspace, do not let that project inject a
	// shell binary; the user/global runtime remains in effect.
	if profileRuntimePath != "" && !trusted && pathWithin(profileRuntimePath, workspacePath) {
		profileRuntimePath = ""
	}
	if profileRuntimePath != "" {
		if sameShellPath(profileRuntimePath, r.globalPath) {
			return r.fallback()
		}
		return r.resolveFile("profile|"+workspacePath+"|"+profileRuntimePath, profileRuntimePath, profileRuntimePath)
	}

	// A skills_runtime.config_file override is server-global by design: it is an
	// explicit user configuration, not project content. When the server already
	// loaded that same file as its global default, keep it for every workspace.
	if r.explicit != "" {
		if sameShellPath(r.explicit, r.globalPath) {
			return r.fallback()
		}
		return r.resolveFile("explicit|"+r.explicit, r.explicit, r.explicit)
	}

	if workspacePath == "" {
		return r.fallback()
	}

	layers := agentconfig.RuntimeConfigLayerStackFrom(workspacePath)
	if !trusted {
		// Untrusted workspace: project layer is ignored, user layer still applies.
		layers = agentconfig.GlobalRuntimeConfigLayerStack()
	}
	fingerprint := runtimeConfigLayerFingerprint(layers)
	cacheKey := "workspace|" + workspacePath + "|trusted=" + boolToken(trusted)
	if runtime, source, ok := r.cached(cacheKey, fingerprint); ok {
		return runtime, source, nil
	}

	document, err := loadRuntimeConfigDocumentForLayers(layers)
	if err != nil {
		return nil, "", err
	}
	sourcePath := strings.TrimSpace(document.SourcePath)
	if sourcePath == "" {
		sourcePath = r.globalPath
	}
	cfg, err := decodeRuntimeShell(document.MergedYAML)
	if err != nil {
		return nil, sourcePath, err
	}
	if sameShellPath(sourcePath, r.globalPath) && r.global != nil &&
		cfg.Fingerprint() == r.global.Config.Fingerprint() {
		r.store(cacheKey, fingerprint, r.global, sourcePath)
		return r.global, sourcePath, nil
	}

	runtime, resolveErr := runtimeexecutor.ResolveShellRuntime(cfg)
	if runtime != nil {
		runtime.ResolveErr = resolveErr
		runtime.SourcePath = sourcePath
		runtime.Fingerprint = runtime.RuntimeFingerprint()
		r.store(cacheKey, fingerprint, runtime, sourcePath)
	}
	return runtime, sourcePath, resolveErr
}

// Invalidate drops every cached entry for one workspace (settings save,
// external file edit, trust change).
func (r *WorkspaceShellResolver) Invalidate(workspacePath string) {
	if r == nil {
		return
	}
	workspacePath = normalizeWorkspaceShellPath(workspacePath)
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.cache {
		if strings.Contains(key, "|"+workspacePath+"|") {
			delete(r.cache, key)
		}
	}
}

// InvalidateAll drops every cached workspace resolution.
func (r *WorkspaceShellResolver) InvalidateAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]*workspaceShellEntry)
}

func (r *WorkspaceShellResolver) resolveFile(cacheKey, sourcePath, filePath string) (*runtimeexecutor.ShellRuntime, string, error) {
	if info, err := os.Stat(filePath); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("runtime config %s is a directory", filePath)
		}
		return nil, filePath, fmt.Errorf("load runtime config %s: %w", filePath, err)
	}
	fingerprint := runtimeConfigFileFingerprint(filePath)
	if runtime, source, ok := r.cached(cacheKey, fingerprint); ok {
		return runtime, source, nil
	}
	document, err := agentconfig.LoadRuntimeConfigDocumentFile(filePath)
	if err != nil {
		return nil, filePath, err
	}
	if document == nil || !documentHasPresentLayers(document) {
		return r.fallback()
	}
	cfg, err := decodeRuntimeShell(document.MergedYAML)
	if err != nil {
		return nil, sourcePath, err
	}
	if sameShellPath(sourcePath, r.globalPath) && r.global != nil &&
		cfg.Fingerprint() == r.global.Config.Fingerprint() {
		r.store(cacheKey, fingerprint, r.global, sourcePath)
		return r.global, sourcePath, nil
	}
	runtime, resolveErr := runtimeexecutor.ResolveShellRuntime(cfg)
	if runtime != nil {
		runtime.ResolveErr = resolveErr
		runtime.SourcePath = sourcePath
		runtime.Fingerprint = runtime.RuntimeFingerprint()
		r.store(cacheKey, fingerprint, runtime, sourcePath)
	}
	return runtime, sourcePath, resolveErr
}

func (r *WorkspaceShellResolver) fallback() (*runtimeexecutor.ShellRuntime, string, error) {
	if r != nil && r.global != nil {
		return r.global, r.globalPath, r.global.ResolveErr
	}
	runtime, err := runtimeexecutor.CurrentShellRuntime()
	return runtime, "", err
}

func (r *WorkspaceShellResolver) cached(cacheKey, fingerprint string) (*runtimeexecutor.ShellRuntime, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.cache[cacheKey]
	if entry == nil || entry.fingerprint != fingerprint || entry.runtime == nil {
		return nil, "", false
	}
	entry.lastUse = time.Now()
	return entry.runtime, entry.sourcePath, true
}

func (r *WorkspaceShellResolver) store(cacheKey, fingerprint string, runtime *runtimeexecutor.ShellRuntime, sourcePath string) {
	if runtime == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[cacheKey] = &workspaceShellEntry{
		fingerprint: fingerprint,
		runtime:     runtime,
		sourcePath:  sourcePath,
		lastUse:     time.Now(),
	}
	if len(r.cache) > r.maxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range r.cache {
			if oldestKey == "" || entry.lastUse.Before(oldest) {
				oldestKey = key
				oldest = entry.lastUse
			}
		}
		delete(r.cache, oldestKey)
	}
}

func decodeRuntimeShell(raw []byte) (runtimeexecutor.ShellConfig, error) {
	var payload struct {
		Shell runtimeexecutor.ShellConfig `yaml:"shell"`
	}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &payload); err != nil {
			return runtimeexecutor.ShellConfig{}, fmt.Errorf("parse runtime.yaml shell section: %w", err)
		}
	}
	if err := runtimeexecutor.ValidateShellConfig(&payload.Shell); err != nil {
		return runtimeexecutor.ShellConfig{}, err
	}
	return payload.Shell, nil
}

func loadRuntimeConfigDocumentForLayers(layers []agentconfig.ConfigLayer) (*agentconfig.MergedConfigDocument, error) {
	// There is no exported layered loader for an arbitrary stack; LoadMerged
	// RuntimeConfigDocumentFrom covers workspace stacks and GlobalRuntime
	// covers the trusted=false case. This keeps a single implementation.
	if len(layers) == 1 && layers[0].Kind == agentconfig.LayerKindUser {
		return agentconfig.LoadGlobalRuntimeConfigDocument()
	}
	// All remaining callers are workspace stacks; the function above already
	// built the same layer list, so use the workspace entry point.
	for _, layer := range layers {
		if layer.Kind == agentconfig.LayerKindProject {
			return agentconfig.LoadMergedRuntimeConfigDocumentFrom(filepath.Dir(filepath.Dir(layer.Path)))
		}
	}
	return agentconfig.LoadGlobalRuntimeConfigDocument()
}

func documentHasPresentLayers(document *agentconfig.MergedConfigDocument) bool {
	return document != nil && len(document.PresentLayers) > 0
}

func runtimeConfigLayerFingerprint(layers []agentconfig.ConfigLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		parts = append(parts, runtimeConfigFileFingerprint(layer.Path)+"|kind="+string(layer.Kind))
	}
	return strings.Join(parts, ";")
}

func runtimeConfigFileFingerprint(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "<empty>"
	}
	info, err := os.Stat(path)
	if err != nil {
		return path + "|missing"
	}
	return fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
}

func normalizeWorkspaceShellPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}

func sameShellPath(left, right string) bool {
	left = normalizeWorkspaceShellPath(left)
	right = normalizeWorkspaceShellPath(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathWithin(path, root string) bool {
	path = normalizeWorkspaceShellPath(path)
	root = normalizeWorkspaceShellPath(root)
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func boolToken(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
