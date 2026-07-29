package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
)

type doctorSearchReport struct {
	ToolkitSearchAvailable bool   `json:"toolkit_search_available"`
	RipgrepAvailable       bool   `json:"ripgrep_available"`
	SelectedBackend        string `json:"selected_backend"`
	Path                   string `json:"path,omitempty"`
	Source                 string `json:"source,omitempty"`
	Version                string `json:"version,omitempty"`
	Error                  string `json:"error,omitempty"`
}

type doctorSearchDeps struct {
	resolve      func() (runtimeripgrep.Resolution, error)
	probeVersion func(context.Context, string) (string, error)
}

func newDoctorSearchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "诊断 grep/glob 的 ripgrep 后端与 builtin 回退",
		Long: `诊断结构化 grep/glob 当前会选择的搜索后端。

解析顺序为 AICLI_RG_PATH、发行包 codex-path/rg、可执行文件相邻 rg、系统 PATH；
即使 rg 不可用，toolkit grep/glob 仍可使用 builtin 回退。`,
		Example: `  aicli doctor search
  aicli doctor search --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			report := inspectDoctorSearch(ctx, doctorSearchDeps{
				resolve:      runtimeripgrep.Resolve,
				probeVersion: probeRipgrepVersion,
			})
			return renderDoctorSearchReport(cmd.OutOrStdout(), report, outputOptions.Format)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出")
	return cmd
}

func inspectDoctorSearch(ctx context.Context, deps doctorSearchDeps) doctorSearchReport {
	report := doctorSearchReport{
		ToolkitSearchAvailable: true,
		SelectedBackend:        "builtin",
	}
	resolution, err := deps.resolve()
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Path = resolution.Path
	report.Source = resolution.Source
	version, err := deps.probeVersion(ctx, resolution.Path)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.RipgrepAvailable = true
	report.SelectedBackend = "rg"
	report.Version = version
	return report
}

func probeRipgrepVersion(ctx context.Context, path string) (string, error) {
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ripgrep health probe failed: %w", err)
	}
	line := strings.TrimSpace(string(output))
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	if line == "" {
		return "", fmt.Errorf("ripgrep health probe returned no version")
	}
	return line, nil
}

func renderDoctorSearchReport(writer io.Writer, report doctorSearchReport, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	status := "available"
	if !report.RipgrepAvailable {
		status = "unavailable"
	}
	if _, err := fmt.Fprintf(writer, "toolkit search: available\nripgrep: %s\nselected backend: %s\n", status, report.SelectedBackend); err != nil {
		return err
	}
	if report.Path != "" {
		fmt.Fprintf(writer, "path: %s\nsource: %s\n", report.Path, report.Source)
	}
	if report.Version != "" {
		fmt.Fprintf(writer, "version: %s\n", report.Version)
	}
	if report.Error != "" {
		fmt.Fprintf(writer, "note: %s\n", report.Error)
	}
	return nil
}
