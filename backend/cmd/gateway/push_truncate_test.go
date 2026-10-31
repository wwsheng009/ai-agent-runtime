package main

// push_truncate_test.go — 「已完成」附带正文的截断边界回归
// （docs/gateway/08-notifications.md §5.1）。
//
// 真机复盘（2026-10-31）：微信收到
//   notify-policy.json: {"global":{"completed_include_last_message":true,"completed_last…
// 头部 200 rune 硬切把代码块里的 JSON 切成了半截——内容本身没错，错在切点。

import (
	"strings"
	"testing"
)

func TestTruncateNotificationTextStopsBeforeCodeBlockSnippet(t *testing.T) {
	// 复刻真机那条回复：标题 + 说明 + 「① 策略确实在生效」 + 代码围栏里的 JSON 行，
	// 让 200 rune 的上限正好落在 JSON 中间。
	header := "## 根因找到了：**内容确实算出来了，但被 IM 通道的投影文案顶掉了**\n\n" +
		"不是策略没生效，也不是没算出正文——是**IM 渲染那一层把正文整段替换掉了**。\n\n" +
		"### 证据链\n\n**① 策略确实在生效（线上复核）**\n```\n"
	jsonLine := `notify-policy.json: {"global":{"completed_include_last_message":true,"completed_last_message_max_chars":200}}`
	text := header + jsonLine + "\n```\n\n**② 源数据确实齐备**\n"

	// 先确认夹具确实复现了真机切点（否则这个用例是假的保障）。
	byteAt := strings.Index(text, jsonLine)
	if byteAt < 0 {
		t.Fatal("夹具失效：夹具里找不到 JSON 行")
	}
	startRune := len([]rune(text[:byteAt])) // 注意：字节下标 ≠ rune 下标
	endRune := startRune + len([]rune(jsonLine))
	if startRune >= 200 || endRune <= 200 {
		t.Fatalf("夹具失效：JSON 行占 rune [%d,%d)，200 不在其中，无法复现切在 JSON 中间", startRune, endRune)
	}
	if len([]rune(text)) <= 200 {
		t.Fatal("夹具失效：整体不足 200 rune，不会触发截断")
	}

	got := truncateNotificationText(text, 200)
	if strings.Contains(got, "notify-policy.json") || strings.Contains(got, `{"global"`) {
		t.Fatalf("不得把 JSON/代码块切在半截：%q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断必须补省略号：%q", got)
	}
	if !strings.HasSuffix(strings.TrimSuffix(got, "…"), "**") {
		t.Fatalf("应停在完整的整行上（末尾为加粗小标题）：%q", got)
	}
	if runes := []rune(got); len(runes) > 201 {
		t.Fatalf("截断不得超上限：%d runes", len(runes))
	}
}

func TestTruncateNotificationTextFallsBackToHardCut(t *testing.T) {
	// 没有换行 → 只能硬切（保持既有语义）。
	single := strings.Repeat("结", 500)
	got := truncateNotificationText(single, 200)
	if len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("无换行文本应按上限硬切：len=%d", len([]rune(got)))
	}
	if !strings.HasPrefix(got, strings.Repeat("结", 200)) {
		t.Fatal("硬切内容错误")
	}

	// 换行太靠前（回退会丢光正文）→ 不回退，仍按上限硬切。
	early := "短\n" + strings.Repeat("结", 500)
	got = truncateNotificationText(early, 200)
	if len([]rune(got)) != 201 {
		t.Fatalf("回退幅度过大时不得回退：len=%d", len([]rune(got)))
	}
}

func TestTruncateNotificationTextLeavesShortTextAlone(t *testing.T) {
	short := "结论：微信通知已经修好"
	if got := truncateNotificationText(short, 200); got != short {
		t.Fatalf("短文本不应被改动：%q", got)
	}
	if got := truncateNotificationText(short, 0); got != short {
		t.Fatalf("max<=0 应回落默认上限（200）且不改短文本：%q", got)
	}
}