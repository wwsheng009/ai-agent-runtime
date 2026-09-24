package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// profileExportOptions 是 `aicli profile export` 的输入。
type profileExportOptions struct {
	Ref    string
	Out    string
	DryRun bool
}

// profileExportResult 是 `aicli profile export` 的输出（text/json 共用）。
type profileExportResult struct {
	Reference string   `json:"reference"`
	Profile   string   `json:"profile,omitempty"`
	Root      string   `json:"root"`
	Format    string   `json:"format"`
	Output    string   `json:"output,omitempty"`
	FileCount int      `json:"file_count"`
	Bytes     int      `json:"bytes"`
	Files     []string `json:"files"`
	Written   bool     `json:"written"`
}

// newProfileExportCommand 创建 `aicli profile export`（§23 G5 / Q21）：
// 把 profile 目录导出为 zip 包，供分享/分发（也可直接提交 .aicli/profiles/ 随仓库分发）。
func newProfileExportCommand(getConfig func() *config.Config) *cobra.Command {
	opts := profileExportOptions{}
	cmd := &cobra.Command{
		Use:   "export <profile>",
		Short: "导出 profile 为 zip 包（分享闭环）",
		Long: `把 profile 目录导出为 zip 包（Batch 13 / §23 G5）。

包内路径即 profile 根下的相对路径（profile.yaml / agents/... / prompts/... / skills/...）。
导出是**只读**动作：不写 profile、不写配置、不碰会话；符号链接与原子写残留（*.tmp）
不进包；有界（256 文件 / 单文件 2MiB / 总 8MiB）。

--out 缺省为当前目录下的 <profile>.zip；指向已存在目录时写入 <目录>/<profile>.zip。
（设计表里的 --output <dir|zip> 与既有 --output「输出格式」重名，CLI 用 --out。）`,
		Example: `  aicli profile export coding
  aicli profile export coding --out dist/coding.zip
  aicli profile export .\profiles\review --dry-run --output json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Ref = args[0]
			opts.Out, _ = cmd.Flags().GetString("out")
			opts.DryRun, _ = cmd.Flags().GetBool("dry-run")

			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile export", "json", err, nil)
			}
			executeStructuredCommand("profile export", outputOptions, func() (profileExportResult, map[string]interface{}, error) {
				result, err := runProfileExportCommand(getConfig(), opts)
				return result, nil, err
			}, func(result profileExportResult) interface{} {
				return result
			}, renderProfileExportText)
		},
	}
	cmd.Flags().String("out", "", "输出 zip 路径（缺省 ./<profile>.zip；目录则写 <目录>/<profile>.zip）")
	cmd.Flags().Bool("dry-run", false, "只列出将打包的文件，不写 zip")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

// runProfileExportCommand 解析 profile → 收集文件 → 打包（--dry-run 时只收集）。
func runProfileExportCommand(cfg *config.Config, opts profileExportOptions) (profileExportResult, error) {
	root, ref, err := resolveProfileExportTarget(cfg, opts.Ref)
	if err != nil {
		return profileExportResult{}, err
	}
	files, err := profilesys.CollectBundleFiles(root)
	if err != nil {
		return profileExportResult{}, err
	}
	var buf bytes.Buffer
	if err := profilesys.WriteBundleZip(&buf, files); err != nil {
		return profileExportResult{}, fmt.Errorf("打包失败：%w", err)
	}
	result := profileExportResult{
		Reference: ref,
		Root:      root,
		Format:    "zip",
		FileCount: len(files),
		Bytes:     buf.Len(),
		Files:     profileBundlePathList(files),
	}
	if spec, specErr := profilesys.LoadProfile(root); specErr == nil && spec != nil {
		result.Profile = strings.TrimSpace(spec.Profile.Name)
	}
	if opts.DryRun {
		return result, nil
	}
	out := resolveProfileExportOutputPath(opts.Out, ref)
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return profileExportResult{}, fmt.Errorf("创建输出目录失败：%w", err)
		}
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return profileExportResult{}, fmt.Errorf("写入 %s 失败：%w", out, err)
	}
	result.Output = out
	result.Written = true
	return result, nil
}

// resolveProfileExportTarget 把 <profile> 解析为 profile 根：
// 路径引用（CLI 语义，目录 + profile.yaml 即可）优先，否则走 config registry，
// 最后兜底 config profiles.root/<ref>。不新建第二套解析逻辑。
func resolveProfileExportTarget(cfg *config.Config, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("profile reference is required")
	}
	if profileRefLooksLikePath(ref) {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return "", "", fmt.Errorf("解析路径 %s 失败：%w", ref, err)
		}
		if !profileRootHasProfileYAML(abs) {
			return "", "", fmt.Errorf("目录 %s 不是 profile 根（缺 profile.yaml）", abs)
		}
		return abs, filepath.Base(abs), nil
	}
	registry := resolveProfileRegistryForCLI(cfg)
	if root, err := registry.Resolve(ref); err == nil && profileRootHasProfileYAML(root) {
		return root, ref, nil
	}
	if cfg != nil && cfg.Profiles != nil {
		candidate := filepath.Join(strings.TrimSpace(cfg.Profiles.Root), ref)
		if profileRootHasProfileYAML(candidate) {
			return candidate, ref, nil
		}
	}
	return "", "", fmt.Errorf("profile %q 未找到（用 aicli profile list 查看可用清单）", ref)
}

// profileRefLooksLikePath 判定引用是否按路径处理（CLI 语义：路径含分隔符、绝对
// 路径或以 . 开头）。API 侧只接受名字，这条只属于 CLI。
func profileRefLooksLikePath(ref string) bool {
	return filepath.IsAbs(ref) || strings.HasPrefix(ref, ".") || strings.ContainsAny(ref, "/\\")
}

// resolveProfileExportOutputPath 解析输出路径：缺省 ./<profile>.zip；
// 指向已存在目录时写 <目录>/<profile>.zip。
func resolveProfileExportOutputPath(out, ref string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ref + ".zip"
	}
	if info, err := os.Stat(out); err == nil && info.IsDir() {
		return filepath.Join(out, ref+".zip")
	}
	return out
}

func profileBundlePathList(files []profilesys.BundleFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func renderProfileExportText(result profileExportResult) {
	if result.Written {
		fmt.Fprintf(os.Stdout, "profile 已导出：%s\n", result.Output)
	} else {
		fmt.Fprintf(os.Stdout, "profile 将导出（--dry-run，未写盘）\n")
	}
	fmt.Fprintf(os.Stdout, "  ref:   %s\n", result.Reference)
	if result.Profile != "" {
		fmt.Fprintf(os.Stdout, "  name:  %s\n", result.Profile)
	}
	fmt.Fprintf(os.Stdout, "  root:  %s\n", result.Root)
	fmt.Fprintf(os.Stdout, "  包大小：%d 字节（%d 文件）\n", result.Bytes, result.FileCount)
	for _, path := range result.Files {
		fmt.Fprintf(os.Stdout, "    %s\n", path)
	}
}
