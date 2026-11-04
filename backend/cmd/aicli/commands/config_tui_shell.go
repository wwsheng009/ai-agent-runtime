package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
)

// runShellSettings 是 `aicli config --tui` 的 Shell 执行环境分区。
//
// 只写可写层：保存前把目标文件路径打出来，避免用户以为改到了仓库里的
// portable 层（docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §10）。
func (t *configTUI) runShellSettings() error {
	for {
		cfg, configPath, layer, writable, loadErr := loadDoctorShellConfig()
		t.printHeader("Shell 执行环境")
		if loadErr != nil {
			t.notice("读取 shell 配置失败: " + loadErr.Error())
		}

		resolution, resolveErr := runtimeexecutor.ProbeShellCandidatesCached(cfg)
		t.renderShellSummary(cfg, configPath, layer, writable, resolution, resolveErr)

		fmt.Fprintln(t.writer)
		fmt.Fprintln(t.writer, "  1. 调整候选顺序（逗号分隔的 ID）")
		fmt.Fprintln(t.writer, "  2. 填写/清除强制路径")
		fmt.Fprintln(t.writer, "  3. 开关自动探测")
		fmt.Fprintln(t.writer, "  4. 重新探测")
		fmt.Fprintln(t.writer, "  5. 修改探针超时")
		fmt.Fprintln(t.writer, "  6. 切换长脚本传递方式（stdin/argv）")
		fmt.Fprintln(t.writer, "  b. 返回")
		input, err := t.prompt("shell> ")
		if err != nil {
			return err
		}

		switch strings.ToLower(input) {
		case "b", "back":
			return nil
		case "1", "order", "prefer":
			updated, ok, err := t.promptShellPreferUpdate(cfg)
			if err != nil {
				return err
			}
			if ok {
				cfg.Prefer = updated
				if err := t.saveShellSettings(cfg, configPath); err != nil {
					t.notice(err.Error())
				}
			}
		case "2", "path":
			updated, changed, err := t.promptStringUpdate("强制 shell 路径（留空清除）", cfg.Path)
			if err != nil {
				return err
			}
			if changed {
				cfg.Path = strings.TrimSpace(updated)
				if err := t.saveShellSettings(cfg, configPath); err != nil {
					t.notice(err.Error())
				}
			}
		case "3", "auto", "autodetect":
			updated, changed, err := t.promptBoolUpdate("自动探测候选链", cfg.ShellAutoDetect())
			if err != nil {
				return err
			}
			if changed {
				cfg.AutoDetect = &updated
				if err := t.saveShellSettings(cfg, configPath); err != nil {
					t.notice(err.Error())
				}
			}
		case "4", "probe", "refresh":
			runtimeexecutor.ResetShellMemo()
			runtimeexecutor.InvalidateShellResolution()
			t.notice("已清空进程内缓存，下一轮将重新探测。")
		case "5", "timeout":
			t.promptShellProbeTimeoutUpdate(&cfg, configPath)
		case "6", "delivery":
			t.promptShellDeliveryUpdate(&cfg, configPath)
		default:
			t.notice("未知选项: " + input)
		}
	}
}

func (t *configTUI) renderShellSummary(cfg runtimeexecutor.ShellConfig, configPath, layer string, writable bool, resolution *runtimeexecutor.ShellResolution, resolveErr error) {
	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintln(t.writer, "配置文件:   (未找到 runtime.yaml，保存将写入用户级路径)")
	} else {
		state := "可写"
		if !writable {
			state = "不可写"
		}
		layerLabel := layer
		if layerLabel == "" {
			layerLabel = "unknown"
		}
		fmt.Fprintf(t.writer, "配置文件:   %s  (%s 层, %s)\n", configPath, layerLabel, state)
	}
	if resolution != nil && strings.TrimSpace(resolution.Shell.Path) != "" {
		version := resolution.Shell.Version
		if version != "" {
			version = " (" + version + ")"
		}
		fmt.Fprintf(t.writer, "当前生效:   %s%s  [%s]\n", shellDisplayID(resolution.Shell), version, resolution.Shell.Dialect)
		fmt.Fprintf(t.writer, "路径:       %s\n", resolution.Shell.Path)
	} else {
		fmt.Fprintln(t.writer, "当前生效:   (无可用 shell)")
	}
	if resolveErr != nil {
		fmt.Fprintf(t.writer, "警告:       %s\n", resolveErr.Error())
	}
	fmt.Fprintf(t.writer, "自动探测:   %t   探针超时: %s   传递方式: %s  严格探针: %t\n",
		cfg.ShellAutoDetect(),
		cfg.EffectiveProbeTimeout(),
		cfg.EffectiveScriptDelivery(),
		cfg.ShellStrictProbe(),
	)

	if resolution == nil || len(resolution.Candidates) == 0 {
		return
	}
	fmt.Fprintln(t.writer, "\n候选状态:")
	for _, candidate := range resolution.Candidates {
		status := strings.ToUpper(candidate.Status)
		detail := candidate.Path
		if detail == "" {
			detail = candidate.Reason
		}
		if candidate.ProbeMS > 0 {
			detail = fmt.Sprintf("%s  (%dms)", detail, candidate.ProbeMS)
		}
		fmt.Fprintf(t.writer, "  [%-4s] %-11s %s\n", status, candidate.ID, detail)
	}
}

func shellDisplayID(shell runtimeexecutor.Shell) string {
	if strings.TrimSpace(shell.ID) != "" {
		return shell.ID
	}
	if shell.Dialect != "" {
		return string(shell.Dialect)
	}
	return shell.String()
}

// promptShellPreferUpdate 接受逗号分隔的稳定 ID 序列。空输入 = 恢复内置缺省链。
func (t *configTUI) promptShellPreferUpdate(cfg runtimeexecutor.ShellConfig) ([]string, bool, error) {
	current := strings.Join(cfg.NormalizedPrefer(), ", ")
	fmt.Fprintf(t.writer, "当前候选顺序: %s\n", emptyIfBlank(current))
	fmt.Fprintf(t.writer, "可用 ID: %s\n", strings.Join(runtimeexecutor.ShellCandidateIDs(), ", "))
	raw, err := t.prompt("新顺序（逗号分隔，留空=内置缺省链）: ")
	if err != nil {
		return nil, false, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true, nil
	}
	parts := strings.Split(raw, ",")
	prefer := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.ToLower(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if !runtimeexecutor.IsKnownShellCandidateID(id) {
			t.notice(fmt.Sprintf("未知候选 ID: %s（可用: %s）", id, strings.Join(runtimeexecutor.ShellCandidateIDs(), ", ")))
			return nil, false, nil
		}
		prefer = append(prefer, id)
	}
	if len(prefer) == 0 {
		t.notice("未提供有效候选 ID，配置未修改。")
		return nil, false, nil
	}
	return prefer, true, nil
}

func (t *configTUI) promptShellProbeTimeoutUpdate(cfg *runtimeexecutor.ShellConfig, configPath string) {
	raw, err := t.prompt(fmt.Sprintf("探针超时（当前 %s，如 3s / 500ms）: ", cfg.EffectiveProbeTimeout()))
	if err != nil {
		t.notice(err.Error())
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	timeout, parseErr := time.ParseDuration(raw)
	if parseErr != nil || timeout <= 0 {
		t.notice("无法解析的时长: " + raw)
		return
	}
	cfg.ProbeTimeout = timeout
	if err := t.saveShellSettings(*cfg, configPath); err != nil {
		t.notice(err.Error())
	}
}

func (t *configTUI) promptShellDeliveryUpdate(cfg *runtimeexecutor.ShellConfig, configPath string) {
	fmt.Fprintf(t.writer, "当前传递方式: %s（stdin = 长脚本走 stdin，argv = 一律拼命令行）\n", cfg.EffectiveScriptDelivery())
	raw, err := t.prompt("新传递方式（stdin/argv）: ")
	if err != nil {
		t.notice(err.Error())
		return
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stdin":
		cfg.ScriptDelivery = runtimeexecutor.ScriptDeliveryStdin
	case "argv":
		cfg.ScriptDelivery = runtimeexecutor.ScriptDeliveryArgv
	case "":
		return
	default:
		t.notice("只接受 stdin 或 argv")
		return
	}
	if err := t.saveShellSettings(*cfg, configPath); err != nil {
		t.notice(err.Error())
	}
}

// saveShellSettings 先校验，再显示目标文件，最后落盘并让进程内解析失效。
func (t *configTUI) saveShellSettings(cfg runtimeexecutor.ShellConfig, configPath string) error {
	if err := runtimeexecutor.ValidateShellConfig(&cfg); err != nil {
		return err
	}
	target := strings.TrimSpace(configPath)
	if target == "" {
		if resolved, _ := agentconfig.RuntimeConfigWriteTarget(); strings.TrimSpace(resolved) != "" {
			target = resolved
		}
	}
	if target == "" {
		return fmt.Errorf("找不到可写的 runtime.yaml 目标文件")
	}

	fmt.Fprintf(t.writer, "将写入: %s\n", target)
	if strings.TrimSpace(cfg.Path) != "" {
		fmt.Fprintf(t.writer, "强制路径: %s\n", cfg.Path)
	}
	if prefer := cfg.NormalizedPrefer(); len(prefer) > 0 {
		fmt.Fprintf(t.writer, "候选顺序: %s\n", strings.Join(prefer, ", "))
	}
	if err := runtimeserver.PersistShellSettings(target, cfg); err != nil {
		return fmt.Errorf("保存失败: %w", err)
	}

	// 立即生效：进程级 resolver 换成新配置，并清掉探测缓存。
	runtimeexecutor.ConfigureShell(cfg)
	runtimeexecutor.InvalidateShellResolution()
	t.notice("已保存并生效。")
	return nil
}
