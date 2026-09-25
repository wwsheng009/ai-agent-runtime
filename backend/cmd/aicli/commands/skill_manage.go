package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type skillListOptions struct {
	Cwd        string
	Debug      bool
	ConfigFile string
}

type skillListEntry struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Root           string `json:"root,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Description    string `json:"description,omitempty"`
	UserInvocable  bool   `json:"user_invocable"`
	ModelInvocable bool   `json:"model_invocable"`
}

type skillListResult struct {
	Cwd      string                  `json:"cwd"`
	Roots    []skill.CodexSkillRoot  `json:"roots,omitempty"`
	Skills   []skillListEntry        `json:"skills"`
	Count    int                     `json:"count"`
	Errors   []skill.CodexSkillError `json:"errors,omitempty"`
	Warnings []skill.CodexSkillError `json:"warnings,omitempty"`
}

type skillRemoveOptions struct {
	Name   string
	Cwd    string
	Dir    string
	Global bool
	DryRun bool
	Force  bool
}

type skillRemoveResult struct {
	Name      string `json:"name"`
	Root      string `json:"root"`
	Path      string `json:"path"`
	Removed   bool   `json:"removed"`
	DryRun    bool   `json:"dry_run"`
	Forced    bool   `json:"forced"`
	FileCount int    `json:"file_count"`
	Message   string `json:"message,omitempty"`
}

func newSkillListCommand() *cobra.Command {
	opts := skillListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出参与发现的 skills（roots/scope/错误/告警）",
		Long: `按 Codex 兼容规则列出 skills 发现面。

默认展示当前工作区的发现结果（工作区 .agents/skills、祖先目录、用户级
~/.agents/skills 等）。--debug 额外展示参与发现的 roots 与 scope，便于定位
"为什么这个 skill 没出现"。`,
		Example: `  aicli skill list
  aicli skill list --debug
  aicli skill list --cwd D:\repo --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			handleSkillListCommand(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Cwd, "cwd", "", "发现锚点目录（默认当前工作目录）")
	cmd.Flags().BoolVar(&opts.Debug, "debug", false, "展示 roots/scope 与不合法条目诊断")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handleSkillListCommand(cmd *cobra.Command, opts skillListOptions) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("skill list", "json", err, nil)
	}
	executeCommand("skill list", outputOptions, func() (skillListResult, map[string]interface{}, error) {
		return runSkillListCommand(opts)
	}, renderSkillListResult)
}

func runSkillListCommand(opts skillListOptions) (skillListResult, map[string]interface{}, error) {
	anchor := strings.TrimSpace(opts.Cwd)
	if anchor == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return skillListResult{}, nil, fmt.Errorf("resolve working directory: %w", err)
		}
		anchor = cwd
	}
	anchor = resolveSkillInstallPath(anchor)

	result := skillListResult{Cwd: anchor, Skills: []skillListEntry{}}
	outcome := skill.DiscoverCodexSkillLoadOutcome(anchor, strings.TrimSpace(opts.ConfigFile), nil)
	result.Errors = outcome.Errors
	result.Warnings = outcome.Warnings
	if opts.Debug {
		result.Roots = skill.DiscoverCodexCompatibleSkillRoots(anchor, strings.TrimSpace(opts.ConfigFile))
	}

	for _, meta := range outcome.Skills {
		if meta == nil {
			continue
		}
		entry := skillListEntry{
			Name:           meta.Name,
			Path:           meta.PathToSkillsMD,
			Description:    meta.Description,
			UserInvocable:  meta.UserInvocableEnabled(),
			ModelInvocable: meta.ImplicitInvocationAllowed(),
			Scope:          meta.Scope,
		}
		entry.Root, entry.Scope = matchSkillRoot(result.Roots, meta.PathToSkillsMD, entry.Scope)
		result.Skills = append(result.Skills, entry)
	}
	sort.SliceStable(result.Skills, func(i, j int) bool {
		if result.Skills[i].Name != result.Skills[j].Name {
			return result.Skills[i].Name < result.Skills[j].Name
		}
		return result.Skills[i].Path < result.Skills[j].Path
	})
	result.Count = len(result.Skills)
	return result, skillListErrorDetails(result), nil
}

// matchSkillRoot 返回 skill 路径所属的根与 scope（最长前缀匹配）。
func matchSkillRoot(roots []skill.CodexSkillRoot, skillPath string, fallbackScope string) (string, string) {
	normalized := resolveSkillInstallPath(filepath.Dir(filepath.Clean(skillPath)))
	bestRoot := ""
	bestScope := fallbackScope
	for _, root := range roots {
		rootPath := resolveSkillInstallPath(root.Path)
		if rootPath == "" {
			continue
		}
		if !samePath(rootPath, normalized) && !strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(rootPath)+string(os.PathSeparator)) {
			continue
		}
		if len(rootPath) > len(bestRoot) {
			bestRoot = rootPath
			bestScope = root.Scope
		}
	}
	return bestRoot, bestScope
}

func renderSkillListResult(result skillListResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("skill list", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("Cwd: %s\n", result.Cwd)
	if len(result.Roots) > 0 {
		fmt.Println("Roots:")
		for _, root := range result.Roots {
			fmt.Printf("  - [%s] %s\n", root.Scope, root.Path)
		}
	}
	if len(result.Skills) == 0 {
		fmt.Println("未发现 skill。")
	}
	for _, item := range result.Skills {
		label := item.Name
		if strings.EqualFold(strings.TrimSpace(item.Scope), "user") {
			label += " (user)"
		}
		fmt.Printf("- %s\n", label)
		fmt.Printf("  path: %s\n", item.Path)
		if item.Description != "" {
			fmt.Printf("  desc: %s\n", item.Description)
		}
	}
	if len(result.Errors) > 0 {
		fmt.Printf("Errors (%d):\n", len(result.Errors))
		for _, item := range result.Errors {
			fmt.Printf("  - %s: %s\n", item.Path, item.Message)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Printf("Warnings (%d):\n", len(result.Warnings))
		for _, item := range result.Warnings {
			fmt.Printf("  - %s: %s\n", item.Path, item.Message)
		}
	}
}

func skillListErrorDetails(result skillListResult) map[string]interface{} {
	return map[string]interface{}{
		"cwd":    result.Cwd,
		"count":  result.Count,
		"skills": len(result.Skills),
	}
}

func newSkillRemoveCommand() *cobra.Command {
	opts := skillRemoveOptions{}
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "删除工作区（或用户级）skills 目录中的 skill",
		Long: `删除指定 skill 目录。

默认只删当前工作区/祖先 <root>/.agents/skills/<name>；-g 删 ~/.agents/skills/<name>。
目录内 SKILL.md 的 frontmatter name 与 <name> 不一致时需要 --force，
避免误删手工改名的目录。`,
		Example: `  aicli skill remove demo-skill --dry-run
  aicli skill remove demo-skill
  aicli skill remove demo-skill -g --force`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Name = args[0]
			handleSkillRemoveCommand(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Cwd, "cwd", "", "工作区锚点目录（默认当前工作目录）")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "直接指定 skills 根目录（覆盖 -g/工作区推导）")
	cmd.Flags().BoolVarP(&opts.Global, "global", "g", false, "操作用户级 ~/.agents/skills")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只显示将删除的目录，不写入文件")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "frontmatter name 不一致时仍删除")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handleSkillRemoveCommand(cmd *cobra.Command, opts skillRemoveOptions) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("skill remove", "json", err, nil)
	}
	executeCommand("skill remove", outputOptions, func() (skillRemoveResult, map[string]interface{}, error) {
		return runSkillRemoveCommand(opts)
	}, renderSkillRemoveResult)
}

func runSkillRemoveCommand(opts skillRemoveOptions) (skillRemoveResult, map[string]interface{}, error) {
	name := strings.TrimSpace(opts.Name)
	if err := validateSkillInstallName(name); err != nil {
		return skillRemoveResult{Name: name}, nil, err
	}
	root, err := resolveSkillManageRoot(opts.Dir, opts.Global, opts.Cwd)
	if err != nil {
		return skillRemoveResult{Name: name}, nil, err
	}
	target := filepath.Join(root, name)
	result := skillRemoveResult{Name: name, Root: root, Path: target, DryRun: opts.DryRun}

	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return result, skillRemoveErrorDetails(result), fmt.Errorf("skill directory not found: %s", target)
	}

	mismatch := skillNameMismatch(target, name)
	if mismatch != "" && !opts.Force {
		return result, skillRemoveErrorDetails(result), fmt.Errorf(
			"%s（确认要删除时加 --force）", mismatch)
	}
	result.Forced = mismatch != ""

	counts, countErr := countSkillDirectory(target)
	if countErr == nil {
		result.FileCount = counts.files
	}
	if opts.DryRun {
		result.Message = "dry-run：未删除任何文件"
		return result, skillRemoveErrorDetails(result), nil
	}
	if err := os.RemoveAll(target); err != nil {
		return result, skillRemoveErrorDetails(result), fmt.Errorf("remove %s: %w", target, err)
	}
	result.Removed = true
	return result, skillRemoveErrorDetails(result), nil
}

// skillNameMismatch 校验目标目录的 SKILL.md frontmatter name 与目录名是否一致。
// 返回空串表示一致（或目录不是 Codex skill 形态，交由调用方决定）。
func skillNameMismatch(dir string, wantName string) string {
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	item, err := skill.NewLoader(nil).LoadFileFull(path)
	if err != nil || item == nil || item.Codex == nil {
		return fmt.Sprintf("无法解析 %s，不能确认它是 %q", path, wantName)
	}
	if !strings.EqualFold(strings.TrimSpace(item.Codex.Name), wantName) {
		return fmt.Sprintf("%s 的 frontmatter name 是 %q，与目录名 %q 不一致", path, item.Codex.Name, wantName)
	}
	return ""
}

func renderSkillRemoveResult(result skillRemoveResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("skill remove", outputOptions.Envelope, result)
		return
	}
	switch {
	case result.DryRun:
		fmt.Printf("计划删除 skill: %s\n", result.Name)
	case result.Removed:
		fmt.Printf("已删除 skill: %s\n", result.Name)
	default:
		fmt.Printf("未删除 skill: %s\n", result.Name)
	}
	fmt.Printf("Root:  %s\n", result.Root)
	fmt.Printf("Path:  %s\n", result.Path)
	fmt.Printf("Files: %d\n", result.FileCount)
	if result.Message != "" {
		fmt.Printf("Message: %s\n", result.Message)
	}
}

func skillRemoveErrorDetails(result skillRemoveResult) map[string]interface{} {
	return map[string]interface{}{
		"name": result.Name,
		"root": result.Root,
		"path": result.Path,
	}
}

// resolveSkillManageRoot 解析 add/remove 的目标 skills 根目录。
func resolveSkillManageRoot(dir string, global bool, cwd string) (string, error) {
	if explicit := strings.TrimSpace(dir); explicit != "" {
		return resolveSkillInstallPath(explicit), nil
	}
	if global {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("cannot resolve home directory for -g")
		}
		return filepath.Join(home, ".agents", "skills"), nil
	}
	anchor := strings.TrimSpace(cwd)
	if anchor == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		anchor = wd
	}
	return filepath.Join(resolveSkillInstallPath(anchor), ".agents", "skills"), nil
}

type skillAddOptions struct {
	Source  string
	Ref     string
	Name    string
	SubPath string
	Cwd     string
	Dir     string
	Global  bool
	DryRun  bool
	Force   bool
}

type skillAddResult struct {
	Source       string `json:"source"`
	Repo         string `json:"repo"`
	RequestedRef string `json:"requested_ref,omitempty"`
	Ref          string `json:"ref,omitempty"`
	RepoPath     string `json:"repo_path,omitempty"`
	SkillName    string `json:"skill_name"`
	TargetRoot   string `json:"target_root"`
	TargetDir    string `json:"target_dir"`
	Installed    bool   `json:"installed"`
	DryRun       bool   `json:"dry_run"`
	Overwritten  bool   `json:"overwritten"`
	FileCount    int    `json:"file_count"`
	Message      string `json:"message,omitempty"`
}

// skillArchiveMaxBytes 限制单次 GitHub tarball 下载大小，避免误拉大仓库。
const skillArchiveMaxBytes = 64 << 20

func newSkillAddCommand() *cobra.Command {
	opts := skillAddOptions{}
	cmd := &cobra.Command{
		Use:   "add <owner/repo>[@ref]",
		Short: "从 GitHub 安装 skill（tarball 拉取 + 校验 + 落盘）",
		Long: `从 GitHub 仓库安装 skill 目录。

默认拉取 <owner>/<repo>（先试 main，再试 master；可用 @ref 或 --ref 指定分支/tag/commit），
解包后定位含 SKILL.md 的目录并安装到 <root>/<name>：

  - 默认 root = 当前工作区 .agents/skills（可用 --dir 覆盖，-g 用 ~/.agents/skills）；
  - 目标已存在时需 --force 才会覆盖；
  - 安装会写入 .aicli-source.json 记录来源 repo/ref/commit（供应链可追溯）；
  - --dry-run 只打印计划，不写文件。`,
		Example: `  aicli skill add openai/skills@main --dry-run
  aicli skill add owner/repo -s my-skill
  aicli skill add owner/repo -g --force`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Source = args[0]
			handleSkillAddCommand(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Ref, "ref", "", "分支/tag/commit（覆盖 <owner/repo>@ref）")
	cmd.Flags().StringVarP(&opts.Name, "name", "s", "", "安装后的 skill 名（默认取 skill 目录名）")
	cmd.Flags().StringVar(&opts.SubPath, "path", "", "仓库内 skill 子目录（默认自动定位含 SKILL.md 的目录）")
	cmd.Flags().StringVar(&opts.Cwd, "cwd", "", "工作区锚点目录（默认当前工作目录）")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "直接指定目标 skills 根目录（覆盖 -g/工作区推导）")
	cmd.Flags().BoolVarP(&opts.Global, "global", "g", false, "安装到用户级 ~/.agents/skills")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只显示安装计划，不写入文件")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "目标已存在时覆盖安装")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handleSkillAddCommand(cmd *cobra.Command, opts skillAddOptions) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("skill add", "json", err, nil)
	}
	executeCommand("skill add", outputOptions, func() (skillAddResult, map[string]interface{}, error) {
		return runSkillAddCommand(opts)
	}, renderSkillAddResult)
}

func runSkillAddCommand(opts skillAddOptions) (skillAddResult, map[string]interface{}, error) {
	owner, repo, inlineRef, err := parseSkillAddSource(opts.Source)
	if err != nil {
		return skillAddResult{Source: opts.Source}, nil, err
	}
	ref := strings.TrimSpace(opts.Ref)
	if ref == "" {
		ref = inlineRef
	}
	result := skillAddResult{
		Source:       opts.Source,
		Repo:         owner + "/" + repo,
		RequestedRef: ref,
		DryRun:       opts.DryRun,
	}

	refs := []string{ref}
	if ref == "" {
		// 未指定 ref：先 main 再 master，覆盖两种默认分支。
		refs = []string{"main", "master"}
	}

	var archive []byte
	var usedRef string
	var lastErr error
	for _, candidate := range refs {
		data, resolvedRef, fetchErr := downloadGitHubArchive(context.Background(), owner, repo, candidate, skillArchiveMaxBytes)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		archive = data
		usedRef = resolvedRef
		break
	}
	if archive == nil {
		return result, skillAddErrorDetails(result), fmt.Errorf("下载 %s 失败: %w", result.Repo, lastErr)
	}
	result.Ref = usedRef

	extractRoot, err := os.MkdirTemp("", "aicli-skill-add-*")
	if err != nil {
		return result, skillAddErrorDetails(result), fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(extractRoot)

	if _, err := extractSkillTarGz(archive, extractRoot); err != nil {
		return result, skillAddErrorDetails(result), err
	}
	sourceDir, repoPath, err := locateSkillDirInArchive(extractRoot, opts.SubPath, opts.Name, repo)
	if err != nil {
		return result, skillAddErrorDetails(result), err
	}
	result.RepoPath = repoPath

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = filepath.Base(sourceDir)
	}
	if err := validateSkillInstallName(name); err != nil {
		return result, skillAddErrorDetails(result), err
	}
	if mismatch := skillNameMismatch(sourceDir, name); mismatch != "" && !opts.Force {
		return result, skillAddErrorDetails(result), fmt.Errorf("%s（确认要安装时加 --force）", mismatch)
	}

	root, err := resolveSkillManageRoot(opts.Dir, opts.Global, opts.Cwd)
	if err != nil {
		return result, skillAddErrorDetails(result), err
	}
	target := filepath.Join(root, name)
	result.SkillName = name
	result.TargetRoot = root
	result.TargetDir = target

	installed, err := installSkillFromTree(sourceDir, name, root, opts.Force, opts.DryRun, skillSourceRecord{
		Repo:        result.Repo,
		Ref:         result.Ref,
		RepoPath:    result.RepoPath,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return result, skillAddErrorDetails(result), err
	}
	result.FileCount = installed.FileCount
	result.Overwritten = installed.Overwritten
	result.Installed = installed.Installed
	result.Message = installed.Message
	return result, skillAddErrorDetails(result), nil
}

// installSkillFromTree 把已解包的 skill 目录安装到 root/<name>。
// 名称一致性、覆盖保护、来源记录都在这里收口，便于单测（无需网络）。
func installSkillFromTree(sourceDir, name, root string, force, dryRun bool, record skillSourceRecord) (skillAddResult, error) {
	result := skillAddResult{
		SkillName:  name,
		TargetRoot: root,
		TargetDir:  filepath.Join(root, name),
		DryRun:     dryRun,
	}
	if mismatch := skillNameMismatch(sourceDir, name); mismatch != "" && !force {
		return result, fmt.Errorf("%s（确认要安装时加 --force）", mismatch)
	}
	if info, statErr := os.Stat(result.TargetDir); statErr == nil && info.IsDir() {
		if !force {
			return result, fmt.Errorf("skill 已存在: %s（覆盖请加 --force）", result.TargetDir)
		}
		result.Overwritten = true
	}
	if counts, countErr := countSkillDirectory(sourceDir); countErr == nil {
		result.FileCount = counts.files
	}
	if dryRun {
		result.Message = "dry-run：未写入任何文件"
		return result, nil
	}
	if result.Overwritten {
		if err := os.RemoveAll(result.TargetDir); err != nil {
			return result, fmt.Errorf("clear existing target: %w", err)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return result, fmt.Errorf("create target root: %w", err)
	}
	if err := copySkillDirectory(sourceDir, result.TargetDir); err != nil {
		return result, fmt.Errorf("install skill: %w", err)
	}
	if err := writeSkillSourceRecord(result.TargetDir, record); err != nil {
		return result, fmt.Errorf("write source record: %w", err)
	}
	result.Installed = true
	return result, nil
}

func renderSkillAddResult(result skillAddResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("skill add", outputOptions.Envelope, result)
		return
	}
	switch {
	case result.DryRun:
		fmt.Printf("计划安装 skill: %s\n", result.SkillName)
	case result.Overwritten:
		fmt.Printf("已覆盖安装 skill: %s\n", result.SkillName)
	case result.Installed:
		fmt.Printf("已安装 skill: %s\n", result.SkillName)
	default:
		fmt.Printf("未安装 skill: %s\n", result.SkillName)
	}
	fmt.Printf("Repo:        %s\n", result.Repo)
	fmt.Printf("Ref:         %s\n", result.Ref)
	fmt.Printf("Repo Path:   %s\n", result.RepoPath)
	fmt.Printf("Target Root: %s\n", result.TargetRoot)
	fmt.Printf("Target Dir:  %s\n", result.TargetDir)
	fmt.Printf("Files:       %d\n", result.FileCount)
	if result.Message != "" {
		fmt.Printf("Message:     %s\n", result.Message)
	}
}

func skillAddErrorDetails(result skillAddResult) map[string]interface{} {
	return map[string]interface{}{
		"source":      result.Source,
		"repo":        result.Repo,
		"ref":         result.Ref,
		"skill_name":  result.SkillName,
		"target_root": result.TargetRoot,
		"target_dir":  result.TargetDir,
	}
}

// parseSkillAddSource 解析 `<owner>/<repo>[@ref]`（也接受 github.com URL 形态）。
func parseSkillAddSource(source string) (owner string, repo string, ref string, err error) {
	value := strings.TrimSpace(source)
	if value == "" {
		return "", "", "", fmt.Errorf("skill source is required (owner/repo[@ref])")
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "github.com/", "git@github.com:"} {
		if strings.HasPrefix(lower, prefix) {
			value = value[len(prefix):]
			lower = strings.ToLower(value)
			break
		}
	}
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")
	if index := strings.LastIndex(value, "@"); index > 0 {
		ref = strings.TrimSpace(value[index+1:])
		value = value[:index]
	}
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", "", fmt.Errorf("invalid skill source %q: expected owner/repo[@ref]", source)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ref, nil
}

// downloadGitHubArchive 拉取 GitHub tarball（codeload），带大小上限。
func downloadGitHubArchive(ctx context.Context, owner, repo, ref string, maxBytes int64) ([]byte, string, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return nil, "", fmt.Errorf("owner and repo are required")
	}
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	url := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, repo, ref)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: %s", url, response.Status)
	}
	limited := io.LimitReader(response.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("archive exceeds %d bytes limit", maxBytes)
	}
	return data, ref, nil
}

// extractSkillTarGz 解包 gzip+tar 到 dest，拒绝路径穿越/符号链接等不安全条目。
func extractSkillTarGz(data []byte, dest string) (int, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gzipReader.Close()

	destRoot := filepath.Clean(dest)
	files := 0
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, fmt.Errorf("tar: %w", err)
		}
		if header == nil {
			continue
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(os.PathSeparator)) || name == ".." {
			return files, fmt.Errorf("unsafe archive entry: %s", header.Name)
		}
		target := filepath.Join(destRoot, name)
		if !strings.HasPrefix(target, destRoot+string(os.PathSeparator)) && target != destRoot {
			return files, fmt.Errorf("archive entry escapes destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return files, err
			}
			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return files, err
			}
			if err := file.Close(); err != nil {
				return files, err
			}
			files++
		default:
			// 符号链接/硬链接/设备文件一律跳过：skill 目录不应依赖它们，
			// 解包阶段也不允许把链接指向仓库外。
			continue
		}
	}
	return files, nil
}

// locateSkillDirInArchive 在解包结果中定位 skill 目录（含 SKILL.md）。
// 返回绝对目录与相对仓库根的路径。
func locateSkillDirInArchive(extractRoot string, subPath string, wantName string, repoName string) (string, string, error) {
	topDir, err := singleArchiveTopDir(extractRoot)
	if err != nil {
		return "", "", err
	}
	if trimmed := strings.Trim(strings.TrimSpace(subPath), "/"); trimmed != "" {
		candidate := filepath.Join(topDir, filepath.FromSlash(trimmed))
		if isCodexSkillDir(candidate) {
			return candidate, relFromRoot(topDir, candidate), nil
		}
		return "", "", fmt.Errorf("--path %q 下没有 SKILL.md", subPath)
	}

	if isCodexSkillDir(topDir) {
		return topDir, ".", nil
	}

	candidates := make([]string, 0, 8)
	_ = filepath.WalkDir(topDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if isCodexSkillDir(path) {
			candidates = append(candidates, path)
			return filepath.SkipDir
		}
		return nil
	})
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("仓库中没有找到含 SKILL.md 的目录（可用 --path 指定）")
	}

	preferred := make([]string, 0, 2)
	for _, candidate := range candidates {
		base := filepath.Base(candidate)
		if (wantName != "" && strings.EqualFold(base, wantName)) || (repoName != "" && strings.EqualFold(base, repoName)) {
			preferred = append(preferred, candidate)
		}
	}
	if len(preferred) == 1 {
		return preferred[0], relFromRoot(topDir, preferred[0]), nil
	}
	if len(candidates) == 1 {
		return candidates[0], relFromRoot(topDir, candidates[0]), nil
	}

	relative := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		relative = append(relative, relFromRoot(topDir, candidate))
	}
	sort.Strings(relative)
	return "", "", fmt.Errorf("仓库中存在多个 skill 目录，请用 --path 指定：%s", strings.Join(relative, ", "))
}

func singleArchiveTopDir(extractRoot string) (string, error) {
	entries, err := os.ReadDir(extractRoot)
	if err != nil {
		return "", fmt.Errorf("read extract dir: %w", err)
	}
	dirs := make([]string, 0, 2)
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(extractRoot, entry.Name()))
		}
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("unexpected archive layout: %d top-level directories", len(dirs))
	}
	return dirs[0], nil
}

func relFromRoot(root string, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

type skillSourceRecord struct {
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	RepoPath    string `json:"repo_path"`
	InstalledAt string `json:"installed_at"`
}

// writeSkillSourceRecord 记录安装来源（供应链可追溯），文件名与 skill 内容隔离。
func writeSkillSourceRecord(targetDir string, record skillSourceRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(targetDir, ".aicli-source.json"), data, 0o644)
}
