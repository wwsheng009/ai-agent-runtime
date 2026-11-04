package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"gopkg.in/yaml.v3"
)

// doctorShellReport is the machine-readable shape of `aicli doctor shell`
// (plan §8.3). Text output is derived from the same struct so the two formats
// cannot drift.
type doctorShellReport struct {
	ConfigPath     string                            `json:"config_path,omitempty"`
	ConfigLayer    string                            `json:"config_layer,omitempty"`
	Writable       bool                              `json:"writable"`
	AutoDetect     bool                              `json:"auto_detect"`
	Prefer         []string                          `json:"prefer"`
	Path           string                            `json:"path,omitempty"`
	ProbeTimeout   string                            `json:"probe_timeout"`
	StrictProbe    bool                              `json:"strict_probe"`
	ScriptDelivery string                            `json:"script_delivery"`
	Selected       *doctorShellSelected              `json:"selected,omitempty"`
	Candidates     []runtimeexecutor.ShellCandidateReport `json:"candidates"`
	Findings       []string                          `json:"findings,omitempty"`
	Error          string                            `json:"error,omitempty"`
}

type doctorShellSelected struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	Dialect string `json:"dialect"`
	Source  string `json:"source"`
	ProbeMS int64  `json:"probe_ms"`
}

func newDoctorShellCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "诊断命令执行所用的 shell（探测链、方言与降级候选）",
		Long: `诊断 agent 命令执行会选中哪个 shell，以及每个候选的探测结果。

解析顺序为 shell.path > shell.prefer > 内置候选链（Windows: git-bash →
msys-bash → busybox → pwsh → powershell → cmd）。每个候选都要通过哨兵探针：
能启动、能执行、且 stdout 含哨兵。WSL 启动器会被守卫规则跳过，除非在
shell.prefer 中显式写出 wsl。

本命令只读，不修改任何配置。`,
		Example: `  aicli doctor shell
  aicli doctor shell --json
  aicli doctor shell --refresh
  aicli doctor shell --probe-only wsl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				return err
			}
			refresh, _ := cmd.Flags().GetBool("refresh")
			probeOnly, _ := cmd.Flags().GetString("probe-only")

			cfg, path, layer, writable, loadErr := loadDoctorShellConfig()
			if probeOnly = strings.TrimSpace(probeOnly); probeOnly != "" {
				if !runtimeexecutor.IsKnownShellCandidateID(probeOnly) {
					return fmt.Errorf("unknown candidate %q (known: %s)", probeOnly, strings.Join(runtimeexecutor.ShellCandidateIDs(), ", "))
				}
				cfg.Prefer = []string{strings.ToLower(probeOnly)}
				cfg.Path = ""
			}
			if refresh {
				runtimeexecutor.ResetShellMemo()
			}

			report := buildDoctorShellReport(cfg, path, layer, writable, loadErr, refresh)
			if err := renderDoctorShellReport(cmd.OutOrStdout(), report, outputOptions.Format); err != nil {
				return err
			}
			// CI and installers need a non-zero exit when strict mode found no
			// usable shell; text output alone is easy to ignore.
			if report.Error != "" && report.StrictProbe {
				return fmt.Errorf("%s", report.Error)
			}
			return nil
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出")
	cmd.Flags().Bool("refresh", false, "跳过进程内缓存，强制重新探测")
	cmd.Flags().String("probe-only", "", "只探测指定候选（git-bash|msys-bash|busybox|wsl|pwsh|powershell|cmd）")
	return cmd
}

// loadDoctorShellConfig reads the merged `shell:` block. A malformed key is
// reported through loadErr but still yields whatever decoded, so the report can
// show the failure instead of printing nothing.
func loadDoctorShellConfig() (runtimeexecutor.ShellConfig, string, string, bool, error) {
	var cfg runtimeexecutor.ShellConfig
	var loadErr error

	document, err := agentconfig.LoadMergedRuntimeConfigDocument()
	if err != nil {
		loadErr = err
	}
	if document != nil && len(document.MergedYAML) > 0 {
		var payload struct {
			Shell runtimeexecutor.ShellConfig `yaml:"shell"`
		}
		if err := yaml.Unmarshal(document.MergedYAML, &payload); err != nil {
			if loadErr == nil {
				loadErr = err
			}
		} else {
			cfg = payload.Shell
		}
	}

	writePath, kind := agentconfig.RuntimeConfigWriteTarget()
	layer := string(kind)
	if document != nil {
		if source := strings.TrimSpace(document.SourcePath); source != "" && strings.TrimSpace(writePath) == "" {
			writePath = source
		}
		if document.DefaultWriteLayer != nil {
			layer = string(document.DefaultWriteLayer.Kind)
		}
	}
	return cfg, writePath, layer, writePath != "", loadErr
}

func buildDoctorShellReport(cfg runtimeexecutor.ShellConfig, path, layer string, writable bool, loadErr error, force bool) doctorShellReport {
	report := doctorShellReport{
		ConfigPath:     path,
		ConfigLayer:    layer,
		Writable:       writable,
		AutoDetect:     cfg.ShellAutoDetect(),
		Prefer:         runtimeexecutor.ShellCandidateChain(cfg),
		Path:           strings.TrimSpace(cfg.Path),
		ProbeTimeout:   cfg.EffectiveProbeTimeout().String(),
		StrictProbe:    cfg.ShellStrictProbe(),
		ScriptDelivery: string(cfg.EffectiveScriptDelivery()),
	}
	if loadErr != nil {
		report.Error = loadErr.Error()
	}

	// Diagnostics must show every candidate, not just the first success: a
	// "why is it not using MY shell" report is the whole point. This path never
	// touches the memo, so --refresh is satisfied by construction.
	resolution, resolveErr := runtimeexecutor.ProbeShellCandidates(cfg)
	if resolution != nil {
		report.Candidates = resolution.Candidates
		if strings.TrimSpace(resolution.Shell.Path) != "" {
			report.Selected = &doctorShellSelected{
				ID:      resolution.Shell.ID,
				Path:    resolution.Shell.Path,
				Version: resolution.Shell.Version,
				Dialect: string(resolution.Shell.Dialect),
				Source:  resolution.Shell.Source,
				ProbeMS: resolution.ProbeMS,
			}
		}
	}
	if resolveErr != nil {
		report.Error = resolveErr.Error()
	}
	report.Findings = doctorShellFindings(report, resolution, resolveErr)
	return report
}

func doctorShellFindings(report doctorShellReport, resolution *runtimeexecutor.ShellResolution, resolveErr error) []string {
	var findings []string
	if report.Selected != nil {
		findings = append(findings, fmt.Sprintf("%s 为当前生效 shell（方言 %s）。", report.Selected.ID, report.Selected.Dialect))
	}
	var wslGuard bool
	var failed []string
	var passed []string
	for _, candidate := range report.Candidates {
		switch {
		case candidate.Guard:
			wslGuard = true
		case candidate.Status == runtimeexecutor.ShellCandidatePass:
			passed = append(passed, candidate.ID)
		case candidate.Status == runtimeexecutor.ShellCandidateFail:
			failed = append(failed, candidate.ID)
		}
	}
	if wslGuard {
		findings = append(findings, "wsl 候选被守卫规则跳过（System32\\bash.exe 是启动器，未安装发行版时静默失败）。如需启用请在 shell.prefer 显式写入 wsl。")
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		findings = append(findings, fmt.Sprintf("探针失败的候选：%s（见 candidates[].reason）。", strings.Join(failed, ", ")))
	}
	if len(passed) > 1 {
		findings = append(findings, fmt.Sprintf("可用候选 %d 个，按配置顺序取第一个。", len(passed)))
	}
	if report.Selected != nil && report.Selected.Dialect == string(runtimeexecutor.ShellDialectPosixNarrow) {
		findings = append(findings, "posix-narrow 方言：模型只能使用 POSIX sh，不能用 [[ ]]、数组或 pipefail。")
	}
	if report.Selected != nil && report.Selected.Dialect == string(runtimeexecutor.ShellDialectPosixFull) {
		findings = append(findings, "长脚本走 stdin（bash -s），不进入 Windows 命令行长度限制。")
	}
	if resolveErr != nil {
		findings = append(findings, "严格模式下没有可用 shell；执行路径会退回 DefaultUserShell，配置需要修正。")
	}
	if resolution != nil && resolution.Shell.Source == runtimeexecutor.ShellSourceFallback {
		findings = append(findings, "当前使用最终兜底 shell（strictProbe=false 或全部探针失败）。")
	}
	return findings
}

func renderDoctorShellReport(writer io.Writer, report doctorShellReport, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	if _, err := fmt.Fprintf(writer, "shell resolution\n"); err != nil {
		return err
	}
	configLine := report.ConfigPath
	if configLine == "" {
		configLine = "(no runtime.yaml layer found; using built-in defaults)"
	}
	writeState := "writable"
	if !report.Writable {
		writeState = "not writable"
	}
	if report.ConfigLayer != "" {
		fmt.Fprintf(writer, "  config:        %s   (%s layer, %s)\n", configLine, report.ConfigLayer, writeState)
	} else {
		fmt.Fprintf(writer, "  config:        %s\n", configLine)
	}
	fmt.Fprintf(writer, "  autoDetect:    %t\n", report.AutoDetect)
	fmt.Fprintf(writer, "  prefer:        %s\n", strings.Join(report.Prefer, ", "))
	if report.Path != "" {
		fmt.Fprintf(writer, "  forced path:   %s\n", report.Path)
	}
	if report.Selected != nil {
		selectedName := report.Selected.ID
		if selectedName == "" {
			selectedName = report.Selected.Dialect
		}
		if report.Selected.Version != "" {
			fmt.Fprintf(writer, "  selected:      %s  (%s)\n", selectedName, report.Selected.Version)
		} else {
			fmt.Fprintf(writer, "  selected:      %s\n", selectedName)
		}
		fmt.Fprintf(writer, "  path:          %s\n", report.Selected.Path)
		fmt.Fprintf(writer, "  dialect:       %s\n", report.Selected.Dialect)
		fmt.Fprintf(writer, "  source:        %s\n", report.Selected.Source)
		fmt.Fprintf(writer, "  probe:         PASS (%d ms)\n", report.Selected.ProbeMS)
	} else {
		fmt.Fprintf(writer, "  selected:      none\n")
	}
	fmt.Fprintf(writer, "  strictProbe:   %t\n", report.StrictProbe)
	fmt.Fprintf(writer, "  scriptDelivery: %s\n", report.ScriptDelivery)
	fmt.Fprintf(writer, "  probeTimeout:  %s\n", report.ProbeTimeout)

	fmt.Fprintf(writer, "\ncandidates\n")
	fmt.Fprintf(writer, "  %-12s %-6s %-6s %s\n", "ID", "STATUS", "PROBE", "PATH / REASON")
	for _, candidate := range report.Candidates {
		status := strings.ToUpper(candidate.Status)
		probe := "-"
		if candidate.ProbeMS > 0 {
			probe = fmt.Sprintf("%dms", candidate.ProbeMS)
		}
		detail := candidate.Path
		if detail == "" {
			detail = candidate.Reason
		} else if candidate.Reason != "" {
			detail = candidate.Path + "  (" + candidate.Reason + ")"
		}
		fmt.Fprintf(writer, "  %-12s %-6s %-6s %s\n", candidate.ID, status, probe, detail)
	}

	if len(report.Findings) > 0 {
		fmt.Fprintf(writer, "\nnotes\n")
		for _, finding := range report.Findings {
			fmt.Fprintf(writer, "- %s\n", finding)
		}
	}
	if report.Error != "" {
		fmt.Fprintf(writer, "\nerror: %s\n", report.Error)
	}
	return nil
}

// doctorShellProbeTimeout is kept for symmetry with future --timeout flags.
const doctorShellProbeTimeout = 3 * time.Second
