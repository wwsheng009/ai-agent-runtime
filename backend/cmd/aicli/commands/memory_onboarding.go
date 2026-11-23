package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"gopkg.in/yaml.v3"
)

type memoryOnboardingResult struct {
	ConfigPath  string   `json:"config_path"`
	ConfigLayer string   `json:"config_layer"`
	Mode        string   `json:"mode"`
	DryRun      bool     `json:"dry_run"`
	Applied     bool     `json:"applied"`
	Changed     []string `json:"changed_fields,omitempty"`
	Conflicting []string `json:"conflicting_fields,omitempty"`
	Message     string   `json:"message"`
}

type memoryOnboardingOptions struct {
	Mode          string
	DryRun        bool
	Force         bool
	RuntimeConfig string
}

type memoryPresetValue struct {
	Path  string
	Value interface{}
}

// NewOnboardingCommand adds explicit presets without mixing runtime.yaml
// settings into `aicli init`, which owns the separate agent config.yaml file.
func NewOnboardingCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "onboarding",
		Short: "安装可选功能 preset",
		Long: `安装可选的 runtime 功能 preset。

runtime.yaml 与 aicli init 管理的 config.yaml 分离。memory preset 默认保持
off；需要先观察自动生成链路可指定 --mode shadow。--mode on 会显式启用记忆工具；
旧 job deadline 修复仍需通过管理员 dry-run/确认后执行。`,
	}
	memoryCmd := &cobra.Command{
		Use:   "memory",
		Short: "配置跨会话记忆 runtime preset",
		Long: `配置跨会话记忆 runtime preset。

默认保持 off，不会启用自动生成或 prompt 注入。需要先观察自动生成链路时可用
--mode shadow；确认安全边界后可用 --mode on 显式启用注入、记忆工具和 durable
turn capture、抽取及启动恢复。旧 job deadline 修复不由 preset 自动开启，需先
通过管理员 dry-run 检查，再显式 apply；过期 job 可能被 worker 标为 failed，
需先检查 jobs/status 再显式 retry。`,
		Example: `  aicli onboarding memory --dry-run
  aicli onboarding memory
  aicli onboarding memory --mode on
  aicli onboarding memory --runtime-config .aicli/runtime.yaml --force`,
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("onboarding memory", "json", err, nil)
			}
			mode, _ := cmd.Flags().GetString("mode")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			force, _ := cmd.Flags().GetBool("force")
			runtimeConfig, _ := cmd.Flags().GetString("runtime-config")
			executeCommand("onboarding memory", outputOptions, func() (memoryOnboardingResult, map[string]interface{}, error) {
				return runMemoryOnboarding(memoryOnboardingOptions{
					Mode: mode, DryRun: dryRun, Force: force, RuntimeConfig: runtimeConfig,
				})
			}, renderMemoryOnboardingResult)
		},
	}
	memoryCmd.Flags().String("mode", "off", "记忆运行模式（off|shadow|on；默认 off）")
	memoryCmd.Flags().Bool("dry-run", false, "只预览将要写入的 runtime 配置，不落盘")
	memoryCmd.Flags().Bool("force", false, "覆盖 preset 涉及的既有 memory 字段；保留其它 runtime 配置")
	memoryCmd.Flags().String("runtime-config", "", "显式指定 runtime.yaml 写入路径")
	memoryCmd.Flags().String("output", "", "输出格式（text|json）")
	memoryCmd.Flags().Bool("json", false, "以 JSON 格式输出")
	cmd.AddCommand(memoryCmd)
	return cmd
}

func runMemoryOnboarding(opts memoryOnboardingOptions) (memoryOnboardingResult, map[string]interface{}, error) {
	result := memoryOnboardingResult{
		Mode:   strings.ToLower(strings.TrimSpace(opts.Mode)),
		DryRun: opts.DryRun,
	}
	if result.Mode == "" {
		result.Mode = "off"
	}
	if result.Mode != "off" && result.Mode != "shadow" && result.Mode != "on" {
		return result, nil, fmt.Errorf("memory onboarding mode must be off, shadow or on")
	}

	document, fallbackPath, layer, err := loadMemoryOnboardingDocument(opts.RuntimeConfig)
	if err != nil {
		return result, nil, err
	}
	result.ConfigPath = fallbackPath
	result.ConfigLayer = layer

	updated := map[string]interface{}{}
	if len(document.MergedYAML) > 0 {
		if err := yaml.Unmarshal(document.MergedYAML, &updated); err != nil {
			return result, nil, fmt.Errorf("decode merged runtime config: %w", err)
		}
	}
	if updated == nil {
		updated = map[string]interface{}{}
	}

	values := memoryOnboardingPreset(result.Mode)
	conflicts := make([]string, 0)
	for _, item := range values {
		current, exists := lookupMemoryOnboardingValue(updated, item.Path)
		if exists && !reflect.DeepEqual(current, item.Value) {
			conflicts = append(conflicts, item.Path)
		}
	}
	sort.Strings(conflicts)
	result.Conflicting = append(result.Conflicting, conflicts...)
	if len(conflicts) > 0 && !opts.Force && !opts.DryRun {
		details := map[string]interface{}{
			"config_path": result.ConfigPath, "config_layer": result.ConfigLayer,
			"conflicting_fields": conflicts,
		}
		return result, details, fmt.Errorf(
			"runtime already sets memory fields (%s); review with --dry-run or pass --force to replace only preset fields",
			strings.Join(conflicts, ", "),
		)
	}

	for _, item := range values {
		current, exists := lookupMemoryOnboardingValue(updated, item.Path)
		if !exists || !reflect.DeepEqual(current, item.Value) {
			result.Changed = append(result.Changed, item.Path)
		}
		agentconfig.SetDocumentValue(updated, item.Path, item.Value)
	}
	sort.Strings(result.Changed)

	plannedYAML, err := yaml.Marshal(updated)
	if err != nil {
		return result, nil, fmt.Errorf("encode planned runtime config: %w", err)
	}
	plannedConfig := runtimecfg.DefaultRuntimeConfig()
	if err := yaml.Unmarshal(plannedYAML, plannedConfig); err != nil {
		return result, nil, fmt.Errorf("decode planned runtime config: %w", err)
	}
	if err := runtimecfg.ValidateRuntimeConfig(plannedConfig); err != nil {
		return result, nil, fmt.Errorf("memory preset does not produce a valid runtime config: %w", err)
	}

	details := map[string]interface{}{
		"config_path": result.ConfigPath, "config_layer": result.ConfigLayer,
		"mode": result.Mode, "dry_run": result.DryRun, "changed_fields": result.Changed,
		"conflicting_fields": result.Conflicting,
		"requires_force":     len(result.Conflicting) > 0,
	}
	if result.DryRun {
		result.Message = "dry run: runtime config was not changed"
		if len(result.Conflicting) > 0 {
			result.Message = "dry run: existing preset fields require --force before applying"
		}
		return result, details, nil
	}
	if len(result.Changed) == 0 {
		result.Message = "memory preset is already configured"
		return result, details, nil
	}

	if _, err := agentconfig.ApplyMergedDocumentChanges(document, updated, fallbackPath); err != nil {
		return result, details, fmt.Errorf("write runtime memory preset: %w", err)
	}
	result.Applied = true
	result.Message = "memory runtime preset applied"
	details["applied"] = true
	return result, details, nil
}

func memoryOnboardingPreset(mode string) []memoryPresetValue {
	values := []memoryPresetValue{
		{Path: "memory.mode", Value: mode},
		{Path: "memory.scope", Value: "project"},
		{Path: "memory.manual.enabled", Value: true},
		{Path: "memory.generation.enabled", Value: true},
		{Path: "memory.generation.trigger", Value: "both"},
		{Path: "memory.generation.turnDigest.enabled", Value: true},
		{Path: "memory.generation.turnDigest.recoveryOnStartup", Value: true},
		{Path: "memory.maintenance.captureExtraction.enabled", Value: true},
		{Path: "memory.consolidation.enabled", Value: true},
		{Path: "memory.injection.enabled", Value: true},
		{Path: "memory.injection.mode", Value: "notes"},
		{Path: "memory.redaction.enabled", Value: true},
		{Path: "memory.redaction.dropOnSecret", Value: true},
	}
	// The read/status/history tools are the user-visible way to verify that
	// Memory is actually usable. Keep them disabled for off/shadow, but make
	// `--mode on` self-consistent with the catalog gate. Destructive operations
	// remain separately guarded by allowForget/allowCorrect/allowRollback.
	if mode == "on" {
		values = append(values, memoryPresetValue{
			Path: "memory.tools.enabled", Value: true,
		})
	}
	return values
}

func loadMemoryOnboardingDocument(runtimeConfigPath string) (*agentconfig.MergedConfigDocument, string, string, error) {
	runtimeConfigPath = strings.TrimSpace(runtimeConfigPath)
	if runtimeConfigPath != "" {
		path := runtimeConfigPath
		if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
			}
		}
		path, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return nil, "", "", fmt.Errorf("resolve runtime config path: %w", err)
		}
		document, err := agentconfig.LoadRuntimeConfigDocumentFile(path)
		return document, path, "explicit", err
	}

	if global := agentconfig.GetGlobalConfig(); global != nil && global.SkillsRuntime != nil &&
		aiclipaths.IsExplicitRuntimeConfigOverride(global.SkillsRuntime.ConfigFile) {
		path := aiclipaths.ResolveRuntimeConfigFilePath(global.SkillsRuntime.ConfigFile)
		path, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return nil, "", "", fmt.Errorf("resolve configured runtime config path: %w", err)
		}
		document, err := agentconfig.LoadRuntimeConfigDocumentFile(path)
		return document, path, "explicit", err
	}

	document, err := agentconfig.LoadMergedRuntimeConfigDocument()
	if err != nil {
		return nil, "", "", err
	}
	if document == nil {
		return nil, "", "", fmt.Errorf("merged runtime config document is unavailable")
	}
	if document.DefaultWriteLayer != nil && strings.TrimSpace(document.DefaultWriteLayer.Path) != "" {
		path := document.DefaultWriteLayer.Path
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		return document, path, string(document.DefaultWriteLayer.Kind), nil
	}
	path, kind := agentconfig.RuntimeConfigWriteTarget()
	if strings.TrimSpace(path) == "" {
		return nil, "", "", fmt.Errorf("runtime config has no writable layer")
	}
	return document, path, string(kind), nil
}

func lookupMemoryOnboardingValue(document map[string]interface{}, dottedPath string) (interface{}, bool) {
	var current interface{} = document
	for _, part := range strings.Split(dottedPath, ".") {
		values, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		var found bool
		for key, value := range values {
			if strings.EqualFold(key, part) {
				current, found = value, true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return current, true
}

func renderMemoryOnboardingResult(result memoryOnboardingResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("onboarding memory", outputOptions.Envelope, result)
		return
	}
	if result.DryRun {
		fmt.Printf("预览：将写入 %s（%s）\n", result.ConfigPath, result.ConfigLayer)
	} else if result.Applied {
		fmt.Printf("已更新 runtime preset：%s（%s）\n", result.ConfigPath, result.ConfigLayer)
	} else {
		fmt.Printf("无需修改：%s（%s）\n", result.ConfigPath, result.ConfigLayer)
	}
	if len(result.Changed) > 0 {
		fmt.Printf("字段：%s\n", strings.Join(result.Changed, ", "))
	}
	if len(result.Conflicting) > 0 {
		fmt.Printf("现有值冲突（应用前需 --force）：%s\n", strings.Join(result.Conflicting, ", "))
	}
	if result.Mode == "shadow" {
		fmt.Println("当前为 shadow：不会向 prompt 注入记忆；如确认启用注入，请使用 --mode on。")
	}
}
