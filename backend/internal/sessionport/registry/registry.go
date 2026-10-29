package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/dsh"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/workbuddy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ReadOptions is the adapter-neutral session selection used by the CLI.
type ReadOptions struct {
	Agent       string
	Root        string
	Instance    string
	SessionID   string
	FilePath    string
	DBPath      string
	ProjectsDir string
	ZipPath     string
	Latest      bool
	View        sessionport.SessionView
}

// Imported is one normalized source session.
type Imported struct {
	Session    *runtimechat.Session
	SourceFile string
	Ref        sessionport.SessionRef
	Warnings   []string
}

// ZipInspection is the adapter-neutral view of an official DSH export ZIP.
// ZIP is currently a DSH-only source format.
type ZipInspection struct {
	AgentID  string                   `json:"agent_id"`
	Sessions []sessionport.SessionRef `json:"sessions"`
	Warnings []string                 `json:"warnings,omitempty"`
}

// ExportOptions is the adapter-neutral aicli → target bundle request.
type ExportOptions struct {
	Agent         string
	SessionID     string
	CWD           string
	Title         string
	CreatedAt     time.Time
	Model         string
	History       []runtimetypes.Message
	BundleDir     string
	SourceSession string
	TargetUser    string
	Compression   string
}

// ExportResult is one normalized target bundle result.
type ExportResult struct {
	SessionID    string
	BundleDir    string
	SessionFile  string
	ManifestFile string
	IndexFile    string
	EventCount   int
	Compression  string
	Warnings     []string
}

// SupportedAgentIDs returns adapters visible to `transfer detect --agent`.
func SupportedAgentIDs() []string {
	return []string{string(sessionport.AgentDeepSeekHarness), string(sessionport.AgentWorkBuddy)}
}

// NormalizeAgent validates aliases and maps the empty value to auto.
func NormalizeAgent(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto", nil
	case string(sessionport.AgentDeepSeekHarness), "dsh":
		return string(sessionport.AgentDeepSeekHarness), nil
	case string(sessionport.AgentWorkBuddy), "workbuddy-ai":
		return string(sessionport.AgentWorkBuddy), nil
	default:
		return "", fmt.Errorf("不支持的 Agent: %s（支持 deepseek-harness|workbuddy）", value)
	}
}

// Detect probes one adapter or auto-detects by content signature. For explicit
// roots, a directory only belongs to WorkBuddy when its SQLite signature is
// present; a WorkBuddy root also contains a runtime sessions/ directory and must
// not be mistaken for DSH.
func Detect(roots []string, agent string) ([]sessionport.Instance, error) {
	normalized, err := NormalizeAgent(agent)
	if err != nil {
		return nil, err
	}
	if normalized != "auto" {
		return detectAgent(normalized, roots)
	}
	if len(roots) == 0 {
		instances, err := dsh.Detect(nil, "")
		if err != nil {
			return nil, err
		}
		wb, wbErr := workbuddy.Detect(nil, "")
		if wbErr == nil {
			instances = append(instances, wb...)
		}
		sortInstances(instances)
		return instances, nil
	}
	instances := make([]sessionport.Instance, 0, len(roots))
	errs := make([]string, 0)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		pathAgent := detectAgentForPath(root, "", "")
		if pathAgent == "" {
			pathAgent = detectAgentForPathFallback(root)
		}
		if pathAgent == "" {
			errs = append(errs, root+": 未识别为 DeepSeek Harness 或 WorkBuddy 数据根")
			continue
		}
		found, err := detectAgent(pathAgent, []string{root})
		if err != nil {
			errs = append(errs, root+": "+err.Error())
			continue
		}
		instances = append(instances, found...)
	}
	if len(instances) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	sortInstances(instances)
	return instances, nil
}

func detectAgent(agent string, roots []string) ([]sessionport.Instance, error) {
	switch agent {
	case string(sessionport.AgentDeepSeekHarness):
		return dsh.Detect(roots, "")
	case string(sessionport.AgentWorkBuddy):
		return workbuddy.Detect(roots, "")
	default:
		return nil, fmt.Errorf("未知 Agent: %s", agent)
	}
}

// DetectInstance resolves one root, with a signature check before invoking the
// adapter. Empty agent means auto.
func DetectInstance(agent, root string, includeEmpty bool, dbPath, projectsDir string) (sessionport.Instance, error) {
	normalized, err := NormalizeAgent(agent)
	if err != nil {
		return sessionport.Instance{}, err
	}
	if normalized == "auto" {
		normalized = detectAgentForPath(root, dbPath, projectsDir)
		if normalized == "" {
			normalized = detectAgentForPathFallback(root)
		}
		if normalized == "" {
			return sessionport.Instance{}, fmt.Errorf("无法识别数据根格式: %s（请显式传 --agent）", root)
		}
	}
	switch normalized {
	case string(sessionport.AgentDeepSeekHarness):
		return dsh.DetectInstance(root, includeEmpty)
	case string(sessionport.AgentWorkBuddy):
		return workbuddy.DetectInstance(root, includeEmpty, dbPath, projectsDir)
	default:
		return sessionport.Instance{}, fmt.Errorf("未知 Agent: %s", normalized)
	}
}

// List dispatches by the detected instance agent.
func List(instance sessionport.Instance) ([]sessionport.SessionRef, error) {
	switch instance.AgentID {
	case string(sessionport.AgentDeepSeekHarness):
		return dsh.List(instance)
	case string(sessionport.AgentWorkBuddy):
		return workbuddy.List(instance)
	default:
		return nil, fmt.Errorf("实例 Agent 未知: %s", instance.AgentID)
	}
}

// InspectZip lists the root sessions in an official DSH export ZIP without
// reading or importing a selected session.
func InspectZip(zipPath string) (*ZipInspection, error) {
	inspected, err := dsh.InspectZip(zipPath)
	if err != nil {
		return nil, err
	}
	return &ZipInspection{
		AgentID:  string(sessionport.AgentDeepSeekHarness),
		Sessions: inspected.Sessions,
		Warnings: inspected.Warnings,
	}, nil
}

// Read resolves a source session through one adapter.
func Read(opts ReadOptions) (*Imported, error) {
	agent := strings.TrimSpace(opts.Agent)
	if agent == "" || strings.EqualFold(agent, "auto") {
		agent = readAgentForOptions(opts)
	}
	normalized, err := NormalizeAgent(agent)
	if err != nil {
		return nil, err
	}
	switch normalized {
	case string(sessionport.AgentDeepSeekHarness):
		imported, err := dsh.Read(dsh.ImportOptions{
			Root:      opts.Root,
			Instance:  opts.Instance,
			SessionID: opts.SessionID,
			ZipPath:   opts.ZipPath,
			Latest:    opts.Latest,
			View:      opts.View,
		})
		if err != nil {
			return nil, err
		}
		return &Imported{
			Session:    imported.Session,
			SourceFile: imported.SourceFile,
			Ref:        imported.Ref,
			Warnings:   imported.Warnings,
		}, nil
	case string(sessionport.AgentWorkBuddy):
		imported, err := workbuddy.Read(workbuddy.ImportOptions{
			Root:        opts.Root,
			Instance:    opts.Instance,
			SessionID:   opts.SessionID,
			FilePath:    opts.FilePath,
			DBPath:      opts.DBPath,
			ProjectsDir: opts.ProjectsDir,
			Latest:      opts.Latest,
			View:        opts.View,
		})
		if err != nil {
			return nil, err
		}
		return &Imported{
			Session:    imported.Session,
			SourceFile: imported.SourceFile,
			Ref:        imported.Ref,
			Warnings:   imported.Warnings,
		}, nil
	default:
		return nil, fmt.Errorf("未知 Agent: %s", normalized)
	}
}

// Export publishes a target-native bundle. Apply/index writes are deliberately
// outside this API for the first release.
func Export(opts ExportOptions) (*ExportResult, error) {
	agent := strings.TrimSpace(opts.Agent)
	if agent == "" {
		agent = string(sessionport.AgentDeepSeekHarness)
	}
	normalized, err := NormalizeAgent(agent)
	if err != nil {
		return nil, err
	}
	switch normalized {
	case string(sessionport.AgentDeepSeekHarness):
		compression := strings.TrimSpace(opts.Compression)
		if compression == "" {
			compression = "none"
		}
		result, err := dsh.ExportBundle(dsh.ExportOptions{
			SessionID:     opts.SessionID,
			CWD:           opts.CWD,
			Title:         opts.Title,
			CreatedAt:     opts.CreatedAt,
			Model:         opts.Model,
			History:       opts.History,
			Compression:   compression,
			BundleDir:     opts.BundleDir,
			SourceSession: opts.SourceSession,
		})
		if err != nil {
			return nil, err
		}
		return &ExportResult{
			SessionID:    result.SessionID,
			BundleDir:    result.BundleDir,
			SessionFile:  result.SessionFile,
			ManifestFile: result.ManifestFile,
			EventCount:   result.EventCount,
			Compression:  result.Compression,
			Warnings:     result.Warnings,
		}, nil
	case string(sessionport.AgentWorkBuddy):
		result, err := workbuddy.ExportBundle(workbuddy.ExportOptions{
			SessionID:     opts.SessionID,
			CWD:           opts.CWD,
			Title:         opts.Title,
			CreatedAt:     opts.CreatedAt,
			Model:         opts.Model,
			History:       opts.History,
			BundleDir:     opts.BundleDir,
			SourceSession: opts.SourceSession,
			TargetUser:    opts.TargetUser,
		})
		if err != nil {
			return nil, err
		}
		return &ExportResult{
			SessionID:    result.SessionID,
			BundleDir:    result.BundleDir,
			SessionFile:  result.SessionFile,
			ManifestFile: result.ManifestFile,
			IndexFile:    result.IndexFile,
			EventCount:   result.EventCount,
			Compression:  "none",
			Warnings:     result.Warnings,
		}, nil
	default:
		return nil, fmt.Errorf("未知或未实现的目标 Agent: %s", normalized)
	}
}

// ResolveInstance maps built-in instance aliases to a data root.
func ResolveInstance(instance string) (string, error) {
	value := strings.TrimSpace(instance)
	if value == "" {
		return "", nil
	}
	lower := strings.ToLower(value)
	switch {
	case lower == string(sessionport.AgentDeepSeekHarness)+":default":
		return dsh.DefaultHome(), nil
	case strings.HasPrefix(lower, string(sessionport.AgentWorkBuddy)) || strings.HasPrefix(lower, "workbuddy-ai"):
		return workbuddy.InstanceRoot(value)
	default:
		return "", fmt.Errorf("未知实例 %q；请用 --root 指定数据根", instance)
	}
}

func readAgentForOptions(opts ReadOptions) string {
	if strings.TrimSpace(opts.ZipPath) != "" {
		return string(sessionport.AgentDeepSeekHarness)
	}
	if strings.TrimSpace(opts.FilePath) != "" {
		return string(sessionport.AgentWorkBuddy)
	}
	if agent := detectAgentForPath(opts.Root, opts.DBPath, opts.ProjectsDir); agent != "" {
		return agent
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(opts.Instance)), string(sessionport.AgentWorkBuddy)) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(opts.Instance)), "workbuddy-ai") {
		return string(sessionport.AgentWorkBuddy)
	}
	return string(sessionport.AgentDeepSeekHarness)
}

func detectAgentForPath(path, dbPath, projectsDir string) string {
	if strings.TrimSpace(dbPath) != "" || strings.TrimSpace(projectsDir) != "" {
		return string(sessionport.AgentWorkBuddy)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	for _, candidate := range []string{clean, filepath.Dir(clean)} {
		if regularFile(filepath.Join(candidate, "workbuddy.db")) && dirExists(filepath.Join(candidate, "projects")) {
			return string(sessionport.AgentWorkBuddy)
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	if base == ".workbuddy" || base == ".workbuddy-ai" || base == "workbuddy.db" {
		return string(sessionport.AgentWorkBuddy)
	}
	if base == "projects" || strings.EqualFold(filepath.Base(filepath.Dir(clean)), "projects") {
		return string(sessionport.AgentWorkBuddy)
	}
	for candidate := filepath.Dir(clean); candidate != filepath.Dir(candidate); candidate = filepath.Dir(candidate) {
		if regularFile(filepath.Join(candidate, "workbuddy.db")) && dirExists(filepath.Join(candidate, "projects")) {
			return string(sessionport.AgentWorkBuddy)
		}
		if strings.EqualFold(filepath.Base(candidate), "projects") {
			return string(sessionport.AgentWorkBuddy)
		}
	}
	if strings.HasPrefix(base, "session.") && strings.Contains(base, ".jsonl") {
		return string(sessionport.AgentDeepSeekHarness)
	}
	if entries, err := os.ReadDir(clean); err == nil {
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if !entry.IsDir() && strings.HasPrefix(name, "session.") && strings.Contains(name, ".jsonl") {
				return string(sessionport.AgentDeepSeekHarness)
			}
		}
	}
	if dirExists(filepath.Join(clean, "sessions")) {
		return string(sessionport.AgentDeepSeekHarness)
	}
	if base == "sessions" || strings.EqualFold(filepath.Base(filepath.Dir(clean)), "sessions") {
		return string(sessionport.AgentDeepSeekHarness)
	}
	return ""
}

func detectAgentForPathFallback(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if regularFile(filepath.Join(clean, "workbuddy.db")) {
		return string(sessionport.AgentWorkBuddy)
	}
	if regularFile(filepath.Join(filepath.Dir(clean), "workbuddy.db")) {
		return string(sessionport.AgentWorkBuddy)
	}
	if dirExists(filepath.Join(clean, "sessions")) || regularFile(filepath.Join(clean, ".dsh")) {
		return string(sessionport.AgentDeepSeekHarness)
	}
	return ""
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sortInstances(instances []sessionport.Instance) {
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Root != instances[j].Root {
			return instances[i].Root < instances[j].Root
		}
		return instances[i].InstanceID < instances[j].InstanceID
	})
}
