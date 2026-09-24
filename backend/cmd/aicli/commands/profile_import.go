package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// profileImportOptions 是 `aicli profile import` 的输入。
type profileImportOptions struct {
	Path   string
	Layer  string
	Name   string
	DryRun bool
}

// profileImportResult 是 `aicli profile import` 的输出（text/json 共用）。
type profileImportResult struct {
	Source    string   `json:"source"`
	Layer     string   `json:"layer"`
	Name      string   `json:"name"`
	Root      string   `json:"root"`
	FileCount int      `json:"file_count"`
	Files     []string `json:"files"`
	Valid     bool     `json:"valid"`
	Imported  bool     `json:"imported"`
	Activated bool     `json:"activated"`
	DryRun    bool     `json:"dry_run,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// newProfileImportCommand 创建 `aicli profile import`（§23 G5 / D28 / Q21）：
// 导入目录或 zip 包为新 profile。
func newProfileImportCommand(getConfig func() *config.Config) *cobra.Command {
	opts := profileImportOptions{}
	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "导入 profile 目录或 zip 包（分享闭环）",
		Long: `把 profile 目录或 zip 包导入到 user/project 层（Batch 13 / §23 G5）。

D28 导入安全纪律（与 API 端点同一套语义，共用同一段执行核心）：
  1. 先物化到层根下的临时目录并跑**同一个 validate**，失败即拒绝（层根不留痕）；
  2. 绝不自动激活：不写 default、不切换会话；
  3. 打印将写入的路径清单（与删除的路径清单同一投影）；
  4. 同名目标冲突即拒绝，不覆盖。

命名（D32）：包内 profile.yaml 的 name 是权威，--name 必须与之一致（导入不改写
profile.yaml；改名请导入后用 rename）。包内没有 name 时必须显式传 --name。`,
		Example: `  aicli profile import ./coding.zip
  aicli profile import ./profiles/review --to project
  aicli profile import ./coding.zip --dry-run --output json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Path = args[0]
			opts.Layer, _ = cmd.Flags().GetString("to")
			opts.Name, _ = cmd.Flags().GetString("name")
			opts.DryRun, _ = cmd.Flags().GetBool("dry-run")

			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile import", "json", err, nil)
			}
			executeStructuredCommand("profile import", outputOptions, func() (profileImportResult, map[string]interface{}, error) {
				result, err := runProfileImportCommand(getConfig(), opts)
				return result, nil, err
			}, func(result profileImportResult) interface{} {
				return result
			}, renderProfileImportText)
		},
	}
	cmd.Flags().String("to", "user", "目标层：user|project")
	cmd.Flags().String("name", "", "目标 profile 名（须与包内 profile.yaml 的 name 一致）")
	cmd.Flags().Bool("dry-run", false, "只预演：校验并列出将写入的路径，不落盘")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

// runProfileImportCommand 读包 → 物化到临时目录 → validate → 原子落位。
func runProfileImportCommand(cfg *config.Config, opts profileImportOptions) (profileImportResult, error) {
	source := strings.TrimSpace(opts.Path)
	if source == "" {
		return profileImportResult{}, fmt.Errorf("导入源不能为空（目录或 zip 文件）")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return profileImportResult{}, fmt.Errorf("解析导入源 %s 失败：%w", source, err)
	}
	files, err := loadProfileBundleFromPath(absSource)
	if err != nil {
		return profileImportResult{}, err
	}

	layer := strings.ToLower(strings.TrimSpace(opts.Layer))
	if layer == "" {
		layer = "user"
	}
	layerRoot, err := profilesys.LayerRoot(layer)
	if err != nil {
		return profileImportResult{}, err
	}
	// 与 API 端点同一落位策略：先物化到层根下的临时目录，validate 通过后原子
	// rename。--dry-run 不落盘：临时目录放系统临时区，连层根都不创建（预演不该
	// 在用户仓库里留下 .aicli/ 空目录）。
	tempBase := ""
	if !opts.DryRun {
		if err := os.MkdirAll(layerRoot, 0o755); err != nil {
			return profileImportResult{}, fmt.Errorf("创建层根目录失败：%w", err)
		}
		tempBase = layerRoot
	}
	tempDir, err := os.MkdirTemp(tempBase, ".import-*")
	if err != nil {
		return profileImportResult{}, fmt.Errorf("创建导入工作目录失败：%w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	paths, err := profilesys.ExtractBundle(tempDir, files)
	if err != nil {
		return profileImportResult{}, err
	}

	// D28-1：跑同一个 validate（CLI 侧就是 `aicli profile validate <path>` 的实现）。
	validation, err := runProfileValidateCommand(cfg, tempDir, "")
	if err != nil {
		return profileImportResult{}, annotateProfileResolveError(err)
	}
	if !validation.Valid {
		return profileImportResult{}, fmt.Errorf("导入包未通过 validate（%d error）：%s（修正后重试，D28）",
			validation.ErrorCount, firstProfileValidateError(validation))
	}

	declared := ""
	if spec, specErr := profilesys.LoadProfile(tempDir); specErr == nil && spec != nil {
		declared = strings.TrimSpace(spec.Profile.Name)
	}
	name, err := profilesys.ResolveBundleProfileName(opts.Name, declared)
	if err != nil {
		return profileImportResult{}, err
	}
	if err := profilesys.ValidateProfileName(name); err != nil {
		return profileImportResult{}, err
	}

	targetRoot := filepath.Join(layerRoot, name)
	result := profileImportResult{
		Source:    absSource,
		Layer:     layer,
		Name:      name,
		Root:      targetRoot,
		FileCount: len(paths),
		Files:     paths,
		Valid:     true,
		DryRun:    opts.DryRun,
		// D28：导入绝不自动激活。
		Activated: false,
		Hint:      "已导入但未激活：设为默认或会话内 /profile use 都是独立动作（D28）",
	}
	if _, statErr := os.Stat(targetRoot); statErr == nil {
		return profileImportResult{}, fmt.Errorf("目标层已存在同名 profile：%s（导入不覆盖：改名或先删除）", targetRoot)
	}
	if opts.DryRun {
		return result, nil
	}
	if err := os.Rename(tempDir, targetRoot); err != nil {
		return profileImportResult{}, fmt.Errorf("导入落位失败：%w", err)
	}
	result.Imported = true
	return result, nil
}

// loadProfileBundleFromPath 读导入源：目录（收集文件）或 zip 文件（安全读包）。
func loadProfileBundleFromPath(path string) ([]profilesys.BundleFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("导入源不存在：%s", path)
	}
	if info.IsDir() {
		return profilesys.CollectBundleFiles(path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("导入源必须是目录或 zip 文件：%s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开导入源失败：%w", err)
	}
	defer func() { _ = file.Close() }()
	return profilesys.ReadBundleZip(file, info.Size())
}

// firstProfileValidateError 取第一条 error 级问题（导入拒绝时要给出可执行原因）。
func firstProfileValidateError(result profileValidateResult) string {
	for _, issue := range result.Issues {
		if issue.Severity != profileValidateSeverityError {
			continue
		}
		if strings.TrimSpace(issue.Path) == "" {
			return issue.Message
		}
		return issue.Path + ": " + issue.Message
	}
	return "validate 未通过"
}

func renderProfileImportText(result profileImportResult) {
	if result.Imported {
		fmt.Fprintf(os.Stdout, "profile 已导入：%s（%s 层）\n", result.Name, result.Layer)
	} else {
		fmt.Fprintf(os.Stdout, "profile 将导入（--dry-run，未写盘）：%s（%s 层）\n", result.Name, result.Layer)
	}
	fmt.Fprintf(os.Stdout, "  源:   %s\n", result.Source)
	fmt.Fprintf(os.Stdout, "  root: %s\n", result.Root)
	fmt.Fprintf(os.Stdout, "  文件（%d）：\n", result.FileCount)
	for _, path := range result.Files {
		fmt.Fprintf(os.Stdout, "    %s\n", path)
	}
	if strings.TrimSpace(result.Hint) != "" {
		fmt.Fprintf(os.Stdout, "  提示：%s\n", result.Hint)
	}
}
