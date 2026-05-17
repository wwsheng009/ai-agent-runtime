package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

const defaultInstallSkillName = "aicli"

type skillInstallOptions struct {
	SkillName string
	SourceDir string
	Target    string
	TargetDir string
	DryRun    bool
	Force     bool
}

type skillInstallResult struct {
	SkillName        string `json:"skill_name"`
	SourceDir        string `json:"source_dir"`
	Target           string `json:"target"`
	TargetRoot       string `json:"target_root"`
	TargetDir        string `json:"target_dir"`
	Installed        bool   `json:"installed"`
	DryRun           bool   `json:"dry_run"`
	AlreadyInstalled bool   `json:"already_installed"`
	Overwritten      bool   `json:"overwritten"`
	FileCount        int    `json:"file_count"`
	DirCount         int    `json:"dir_count"`
	Message          string `json:"message"`
}

// NewSkillCommand creates commands for installing Codex-style skill folders.
func NewSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "skill",
		Aliases: []string{"skills"},
		Short:   "管理 Codex 风格 skill",
		Long:    "安装项目内置或外部 Codex 风格 skill 目录（包含 SKILL.md）到目标工具的 skills 根目录。",
	}
	cmd.AddCommand(newSkillInstallCommand())
	return cmd
}

func newSkillInstallCommand() *cobra.Command {
	opts := skillInstallOptions{}
	cmd := &cobra.Command{
		Use:   "install [name]",
		Short: "安装 skill 到目标工具目录",
		Long: `安装 Codex 风格 skill 目录。

默认安装项目内置 aicli skill。source-dir 可以是某个 skill 目录，也可以是包含多个 skill 子目录的 skills 根目录。`,
		Example: `  aicli skill install
  aicli skill install aicli --target codex
  aicli skill install aicli --target aicli
  aicli skill install aicli --target workspace
  aicli skill install aicli --target-dir C:\Users\me\.codex\skills
  aicli skill install aicli --source-dir .\.agents\skills --dry-run --output json`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) > 0 {
				opts.SkillName = args[0]
			}
			handleSkillInstallCommand(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.SourceDir, "source-dir", "", "源 skill 目录或 skills 根目录（留空时从当前仓库向上查找 .agents/skills/<name>）")
	cmd.Flags().StringVar(&opts.Target, "target", "codex", "目标工具（codex|aicli|workspace；被 --target-dir 覆盖）")
	cmd.Flags().StringVar(&opts.TargetDir, "target-dir", "", "目标 skills 根目录；最终安装到 <target-dir>/<name>")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只显示将执行的安装计划，不写入文件")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "目标已存在时覆盖")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handleSkillInstallCommand(cmd *cobra.Command, opts skillInstallOptions) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("skill install", "json", err, nil)
	}
	executeCommand("skill install", outputOptions, func() (skillInstallResult, map[string]interface{}, error) {
		return runSkillInstallCommand(opts)
	}, renderSkillInstallResult)
}

func runSkillInstallCommand(opts skillInstallOptions) (skillInstallResult, map[string]interface{}, error) {
	result := skillInstallResult{
		SkillName: strings.TrimSpace(opts.SkillName),
		Target:    strings.ToLower(strings.TrimSpace(opts.Target)),
		DryRun:    opts.DryRun,
	}
	if result.SkillName == "" {
		result.SkillName = defaultInstallSkillName
	}
	if result.Target == "" {
		result.Target = "codex"
	}
	if strings.TrimSpace(opts.TargetDir) != "" {
		result.Target = "custom"
	}
	if err := validateSkillInstallName(result.SkillName); err != nil {
		return result, nil, err
	}

	sourceDir, err := resolveSkillInstallSourceDir(result.SkillName, opts.SourceDir)
	if err != nil {
		return result, skillInstallErrorDetails(result), err
	}
	targetRoot, err := resolveSkillInstallTargetRoot(result.Target, opts.TargetDir)
	if err != nil {
		return result, skillInstallErrorDetails(result), err
	}
	targetDir := filepath.Join(targetRoot, result.SkillName)

	result.SourceDir = sourceDir
	result.TargetRoot = targetRoot
	result.TargetDir = targetDir

	if samePath(sourceDir, targetDir) {
		counts, countErr := countSkillDirectory(sourceDir)
		if countErr != nil {
			return result, skillInstallErrorDetails(result), countErr
		}
		result.FileCount = counts.files
		result.DirCount = counts.dirs
		result.AlreadyInstalled = true
		result.Message = "source and target are the same"
		return result, skillInstallErrorDetails(result), nil
	}

	targetInfo, statErr := os.Stat(targetDir)
	targetExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return result, skillInstallErrorDetails(result), statErr
	}
	if targetExists && !targetInfo.IsDir() {
		return result, skillInstallErrorDetails(result), fmt.Errorf("target exists and is not a directory: %s", targetDir)
	}
	if targetExists && !opts.Force {
		return result, skillInstallErrorDetails(result), fmt.Errorf("target skill already exists: %s (use --force to overwrite)", targetDir)
	}

	counts, err := countSkillDirectory(sourceDir)
	if err != nil {
		return result, skillInstallErrorDetails(result), err
	}
	result.FileCount = counts.files
	result.DirCount = counts.dirs
	result.Overwritten = targetExists && opts.Force

	if opts.DryRun {
		result.Message = "dry run: skill would be installed"
		return result, skillInstallErrorDetails(result), nil
	}

	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return result, skillInstallErrorDetails(result), err
	}
	if targetExists {
		if err := os.RemoveAll(targetDir); err != nil {
			return result, skillInstallErrorDetails(result), err
		}
	}
	if err := copySkillDirectory(sourceDir, targetDir); err != nil {
		return result, skillInstallErrorDetails(result), err
	}
	result.Installed = true
	if result.Overwritten {
		result.Message = "skill overwritten"
	} else {
		result.Message = "skill installed"
	}
	return result, skillInstallErrorDetails(result), nil
}

func renderSkillInstallResult(result skillInstallResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("skill install", outputOptions.Envelope, result)
		return
	}
	switch {
	case result.DryRun:
		fmt.Printf("计划安装 skill: %s\n", result.SkillName)
	case result.AlreadyInstalled:
		fmt.Printf("skill 已在目标目录: %s\n", result.SkillName)
	case result.Overwritten:
		fmt.Printf("已覆盖安装 skill: %s\n", result.SkillName)
	default:
		fmt.Printf("已安装 skill: %s\n", result.SkillName)
	}
	fmt.Printf("Source:      %s\n", result.SourceDir)
	fmt.Printf("Target Root: %s\n", result.TargetRoot)
	fmt.Printf("Target Dir:  %s\n", result.TargetDir)
	fmt.Printf("Files:       %d\n", result.FileCount)
	if strings.TrimSpace(result.Message) != "" {
		fmt.Printf("Message:     %s\n", result.Message)
	}
}

func validateSkillInstallName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("invalid skill name: %s", name)
	}
	return nil
}

func resolveSkillInstallSourceDir(skillName string, sourceDir string) (string, error) {
	if sourceDir = strings.TrimSpace(sourceDir); sourceDir != "" {
		resolved := resolveSkillInstallPath(sourceDir)
		if isCodexSkillDir(resolved) {
			return resolved, nil
		}
		nested := filepath.Join(resolved, skillName)
		if isCodexSkillDir(nested) {
			return nested, nil
		}
		return "", fmt.Errorf("source skill not found: %s (expected SKILL.md or %s/SKILL.md)", resolved, skillName)
	}
	if found := findBundledSkillDir(skillName); found != "" {
		return found, nil
	}
	return "", fmt.Errorf("bundled skill not found: %s (pass --source-dir)", skillName)
}

func resolveSkillInstallTargetRoot(target string, targetDir string) (string, error) {
	if targetDir = strings.TrimSpace(targetDir); targetDir != "" {
		return resolveSkillInstallPath(targetDir), nil
	}
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "codex":
		if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
			return resolveSkillInstallPath(filepath.Join(home, "skills")), nil
		}
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("cannot resolve home directory for codex target")
		}
		return filepath.Join(home, ".codex", "skills"), nil
	case "aicli":
		if home := strings.TrimSpace(os.Getenv("AICLI_HOME")); home != "" {
			return resolveSkillInstallPath(filepath.Join(home, "skills")), nil
		}
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("cannot resolve home directory for aicli target")
		}
		return filepath.Join(home, ".aicli", "skills"), nil
	case "workspace", "repo", "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("invalid target: %s (expected codex|aicli|workspace)", target)
	}
}

func findBundledSkillDir(skillName string) string {
	seen := map[string]struct{}{}
	addCandidateRoot := func(start string) []string {
		start = strings.TrimSpace(start)
		if start == "" {
			return nil
		}
		start = resolveSkillInstallPath(start)
		info, err := os.Stat(start)
		if err == nil && !info.IsDir() {
			start = filepath.Dir(start)
		}
		var roots []string
		for dir := start; dir != ""; {
			if _, exists := seen[dir]; !exists {
				seen[dir] = struct{}{}
				roots = append(roots, dir)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return roots
	}

	var starts []string
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, exe)
	}

	for _, start := range starts {
		for _, root := range addCandidateRoot(start) {
			candidate := filepath.Join(root, ".agents", "skills", skillName)
			if isCodexSkillDir(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func resolveSkillInstallPath(path string) string {
	path = aiclipaths.ExpandUserPath(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func isCodexSkillDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	info, err = os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}

type skillInstallCounts struct {
	files int
	dirs  int
}

func countSkillDirectory(sourceDir string) (skillInstallCounts, error) {
	counts := skillInstallCounts{}
	err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		if entry.IsDir() {
			counts.dirs++
		} else {
			counts.files++
		}
		return nil
	})
	return counts, err
}

func copySkillDirectory(sourceDir, targetDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, info.Mode().Perm())
	})
}

func samePath(left, right string) bool {
	left = resolveSkillInstallPath(left)
	right = resolveSkillInstallPath(right)
	if strings.EqualFold(left, right) {
		return true
	}
	return left == right
}

func skillInstallErrorDetails(result skillInstallResult) map[string]interface{} {
	return map[string]interface{}{
		"skill_name":  result.SkillName,
		"source_dir":  result.SourceDir,
		"target":      result.Target,
		"target_root": result.TargetRoot,
		"target_dir":  result.TargetDir,
	}
}
