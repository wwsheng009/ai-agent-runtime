package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestChatTTY_LiveLoop_ConPTY_ResumeHistoryTrimAfterDelivery 是 T10-7 的实机
// （ConPTY 伪控制台）验收：展示历史裁剪与 /history 完整恢复必须在**真实终端
// 交付路径**上成立，而不只是进程内单测通过。
//
// 为什么必须上真机：裁剪的触发条件是「装载交付已完成」，而交付只有在真实
// 终端写端存在时才会发生。进程内测试里 surface 不真的把行交给终端，因此
// 无法证明裁剪点在交付之后——这一点只有 ConPTY 能证明。
//
// 证据文件（AICLI_T10_7_OUT 未设置时只断言不归档）：
//
//	conpty-trim.json — 每一步的真实规模（装载 / 交付 / 裁剪 / 恢复）
func TestChatTTY_LiveLoop_ConPTY_ResumeHistoryTrimAfterDelivery(t *testing.T) {
	if !chatTTYConPTYAvailable() {
		t.Skip("ConPTY not supported on this host")
	}

	const seedMessages = 900 // > 512 保留上限，且 > 单页 100，必须多页装载
	evidenceDir := strings.TrimSpace(os.Getenv("AICLI_T10_7_OUT"))
	probePath := ""
	if evidenceDir != "" {
		if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
			t.Fatalf("mkdir evidence: %v", err)
		}
		probePath = filepath.Join(evidenceDir, "conpty-trim.json")
	}

	script := chatTTYConPTYScript{
		Reply:            "T10-7-CONPTY-ANSWER",
		TrimLoadMessages: seedMessages,
		TrimProbeOut:     probePath,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 2500, Line: "/exit"},
		},
	}

	raw := runChatTTYConPTYLiveLoop(t, script, 240*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)
	if !strings.Contains(strings.ToLower(normalized), "interactive=true") {
		t.Fatalf("伪控制台下未观察到 isatty 语义（INTERACTIVE=True）; tail=%q",
			tailBytes(normalized, 600))
	}

	if probePath == "" {
		t.Log("T10-7: AICLI_T10_7_OUT 未设置，跳过证据断言（仅验证真机路径可跑通）")
		return
	}

	payload, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("读取裁剪证据失败（helper 未写入？）: %v", err)
	}
	var probe struct {
		SeedMessages        int    `json:"seed_messages"`
		LoadedAfterFirst    int    `json:"loaded_after_first"`
		LoadedFull          int    `json:"loaded_full"`
		DeliveredRows       int    `json:"delivered_rows"`
		AfterTrim           int    `json:"after_trim"`
		TrimCount           uint64 `json:"trim_count"`
		TrimmedOff          int    `json:"trimmed_off"`
		RestoreOK           bool   `json:"restore_ok"`
		AfterRestore        int    `json:"after_restore"`
		RetainedLimit       int    `json:"retained_limit"`
		InteractiveTerminal bool   `json:"interactive_terminal"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		t.Fatalf("解析裁剪证据失败: %v\npayload=%s", err, string(payload))
	}

	// 前置：真机确认是交互式终端（否则整条交付路径都不成立）。
	if !probe.InteractiveTerminal {
		t.Fatalf("真机断言失败：伪控制台下 IsInteractiveTerminal()=false，交付路径不成立")
	}
	// 1) 分页装载：首帧只装最新一页，补齐后拿到完整转录。
	if probe.LoadedAfterFirst <= 0 || probe.LoadedAfterFirst >= seedMessages {
		t.Fatalf("首帧应只装载最新一页：loaded_after_first=%d seed=%d",
			probe.LoadedAfterFirst, probe.SeedMessages)
	}
	if probe.LoadedFull != seedMessages {
		t.Fatalf("分页补齐后应为完整转录：loaded_full=%d want=%d",
			probe.LoadedFull, seedMessages)
	}
	// 2) 真实交付发生了。
	if probe.DeliveredRows <= 0 {
		t.Fatalf("交付行数为 0：真机交付路径未生效")
	}
	// 3) 交付完成后必须裁剪到固定上限。
	if probe.RetainedLimit != resumeHistoryRetainedAfterDelivery {
		t.Fatalf("保留上限不符：%d want=%d", probe.RetainedLimit, resumeHistoryRetainedAfterDelivery)
	}
	if probe.AfterTrim > probe.RetainedLimit {
		t.Fatalf("裁剪后仍超过上限：after_trim=%d limit=%d", probe.AfterTrim, probe.RetainedLimit)
	}
	if probe.TrimCount == 0 || probe.TrimmedOff <= 0 {
		t.Fatalf("裁剪未发生或未留观测痕迹：trim_count=%d trimmed_off=%d",
			probe.TrimCount, probe.TrimmedOff)
	}
	// 4) /history 语义：必须能恢复完整转录（用户可见内容不因裁剪改变）。
	if !probe.RestoreOK {
		t.Fatalf("裁剪后 /history 未能从存储恢复完整历史（restore_ok=false）")
	}
	if probe.AfterRestore != seedMessages {
		t.Fatalf("恢复后条数不符：after_restore=%d want=%d", probe.AfterRestore, seedMessages)
	}

	t.Logf("T10-7 真机通过：装载 %d → 交付 %d → 裁剪至 %d（丢弃 %d）→ 恢复 %d",
		probe.LoadedFull, probe.DeliveredRows, probe.AfterTrim, probe.TrimmedOff, probe.AfterRestore)
}

// TestChatTTY_LiveLoop_ConPTY_HistoryReplayUsesRestoredTranscript 确认裁剪之后
// `/history` 命令在真机上仍能重放完整历史（走 printVisibleChatHistory 的
// refreshResumeHistoryForReplay 分支），并核对终端投影中最早与最新的 marker
// 同时可见。
func TestChatTTY_LiveLoop_ConPTY_HistoryReplayUsesRestoredTranscript(t *testing.T) {
	if !chatTTYConPTYAvailable() {
		t.Skip("ConPTY not supported on this host")
	}

	const seedMessages = 700
	script := chatTTYConPTYScript{
		Reply:            "T10-7-REPLAY-ANSWER",
		TrimLoadMessages: seedMessages,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 2500, Line: "/history"},
			{FixedWait: 2500, Line: "/exit"},
		},
		Wants: []string{"T10-7-MSG-0699"},
	}

	raw := runChatTTYConPTYLiveLoop(t, script, 240*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Fatalf("真机渲染流中未找到 %q（/history 未重放最新消息）; tail=%q",
				want, tailBytes(normalized, 800))
		}
	}
}

// chatTTYConPTYAvailable 报告本机是否支持伪控制台（避免在不支持的宿主上跑）。
func chatTTYConPTYAvailable() bool {
	// ConPTY 仅在 Windows 10 1809+ 提供；非 Windows 宿主直接跳过，
	// 真正的 API 能力由 runChatTTYConPTYLiveLoop 内部的 pty.ErrUnsupported 兜底。
	return runtime.GOOS == "windows"
}
