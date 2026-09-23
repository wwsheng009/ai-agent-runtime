package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// 方案 §6.1/§6.2/B6：TUI 投影与状态栏 routing 段的单测。

func routingTestBoolPtr(value bool) *bool { return &value }

func routingTestStringPtr(value string) *string { return &value }

func routingTestStringsPtr(values ...string) *[]string { return &values }

// routingStatusTestSession 构造带（可选）会话覆盖的最小 ChatSession。
func routingStatusTestSession(t *testing.T, override *agentconfig.AICLISessionRoutingOverride, cfg *agentconfig.AICLIConfig) *ChatSession {
	t.Helper()
	runtimeSession := &runtimechat.Session{}
	runtimeSession.Metadata.Context = map[string]interface{}{}
	if override != nil {
		raw, err := json.Marshal(override)
		if err != nil {
			t.Fatalf("marshal override: %v", err)
		}
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.LegacyAICLIRoutingOverride, string(raw))
	}
	return &ChatSession{
		Config:         &agentconfig.Config{AICLI: cfg},
		RuntimeSession: runtimeSession,
	}
}

// TestChatRoutingStatusSegmentDisabledShowsRouteOff 钉住关闭态可见性（§6.2）：
// 已配置但关闭 → 不隐藏该段（用户要能看出路由被关掉）；四层都没有任何配置 →
// 段为空（默认关闭零行为变化，不白占状态栏宽度）。
func TestChatRoutingStatusSegmentDisabledShowsRouteOff(t *testing.T) {
	unconfigured := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	if segment := chatSurfaceRoutingStatusSegment(unconfigured); segment.full != "" {
		t.Fatalf("未配置任何层时不应显示路由段，得到 %q", segment.full)
	}

	disabledCfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{Enabled: false},
		},
	}
	session := routingStatusTestSession(t, nil, disabledCfg)
	segment := chatSurfaceRoutingStatusSegment(session)
	if segment.full != "route:off" {
		t.Fatalf("关闭态必须显示 route:off，得到 %q", segment.full)
	}
}

// TestChatStatusBoxRoutingRow 覆盖 §6.3：/status 行表新增 Routing 行，与状态栏
// 段同源（单一投影）；关闭态为 off (source)。
func TestChatStatusBoxRoutingRow(t *testing.T) {
	override := &agentconfig.AICLISessionRoutingOverride{
		MainAgent: &agentconfig.AICLISessionMainAgentRoutingOverride{
			Enabled:           routingTestBoolPtr(true),
			Levels:            routingTestStringsPtr("hard"),
			DefaultDifficulty: routingTestStringPtr("hard"),
			Profiles: map[string]agentconfig.AICLISessionRouteProfileOverride{
				"hard": {
					Model:           routingTestStringPtr("claude-opus-4"),
					ReasoningEffort: routingTestStringPtr("xhigh"),
				},
			},
		},
	}
	enabled := routingStatusTestSession(t, override, &agentconfig.AICLIConfig{})
	if got := buildChatStatusRoutingValue(enabled); got != "on · hard · claude-opus-4 · xhigh (session)" {
		t.Fatalf("enabled Routing 行 = %q", got)
	}
	joined := strings.Join(buildChatStatusBoxLines(enabled, chatStatusDefaultContentWidth), "\n")
	if !strings.Contains(joined, "Routing:") {
		t.Fatalf("/status 必须包含 Routing 行：%s", joined)
	}

	disabled := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	if got := buildChatStatusRoutingValue(disabled); got != "off (default)" {
		t.Fatalf("关闭态 Routing 行 = %q，期望 off (default)", got)
	}
}

// TestChatWebStatusBarSnapshotIncludesRoutingSegment 覆盖 §6.4：micro web client
// 状态栏快照与 TUI 状态栏段同源；零配置时不显示路由段。
func TestChatWebStatusBarSnapshotIncludesRoutingSegment(t *testing.T) {
	override := &agentconfig.AICLISessionRoutingOverride{
		MainAgent: &agentconfig.AICLISessionMainAgentRoutingOverride{
			Enabled:           routingTestBoolPtr(true),
			Levels:            routingTestStringsPtr("hard"),
			DefaultDifficulty: routingTestStringPtr("hard"),
			Profiles: map[string]agentconfig.AICLISessionRouteProfileOverride{
				"hard": {Model: routingTestStringPtr("claude-opus-4")},
			},
		},
	}
	session := routingStatusTestSession(t, override, &agentconfig.AICLIConfig{})
	snap := buildChatWebStatusBarSnapshotForSession(session)
	if snap.Routing != "route:hard · claude-opus-4 · *" {
		t.Fatalf("web 快照 routing = %q", snap.Routing)
	}
	found := false
	for _, seg := range snap.Segments {
		if seg.Kind == string(chatWebStatusBarSegRouting) {
			found = true
		}
	}
	if !found {
		t.Fatalf("web 快照必须包含 routing 段：%+v", snap.Segments)
	}

	unconfigured := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	if got := buildChatWebStatusBarSnapshotForSession(unconfigured).Routing; got != "" {
		t.Fatalf("零配置时不应显示路由段，得到 %q", got)
	}
}

// TestChatRoutingStatusSegmentSessionOverride 覆盖 §6.2 段格式与来源后缀：
// session 层覆盖 → `route:hard · claude-opus-4 · xhigh · *`。
func TestChatRoutingStatusSegmentSessionOverride(t *testing.T) {
	override := &agentconfig.AICLISessionRoutingOverride{
		MainAgent: &agentconfig.AICLISessionMainAgentRoutingOverride{
			Enabled:           routingTestBoolPtr(true),
			Levels:            routingTestStringsPtr("easy", "normal", "hard"),
			DefaultDifficulty: routingTestStringPtr("hard"),
			Profiles: map[string]agentconfig.AICLISessionRouteProfileOverride{
				"hard": {
					Provider:        routingTestStringPtr("anthropic"),
					Model:           routingTestStringPtr("claude-opus-4"),
					ReasoningEffort: routingTestStringPtr("xhigh"),
				},
			},
		},
		UpdatedBy: "tui",
	}
	session := routingStatusTestSession(t, override, &agentconfig.AICLIConfig{})

	segment := chatSurfaceRoutingStatusSegment(session)
	want := "route:hard · claude-opus-4 · xhigh · *"
	if segment.full != want {
		t.Fatalf("session 覆盖段 = %q，期望 %q", segment.full, want)
	}
	if !strings.Contains(segment.compact, "rt:hard") {
		t.Fatalf("compact 形态应为 rt:<level>，得到 %q", segment.compact)
	}

	projection := chatRoutingStatusProjection(session)
	if !projection.Enabled || projection.Level != "hard" {
		t.Fatalf("投影档位不符：%+v", projection)
	}
	if projection.Model != "claude-opus-4" || projection.Reasoning != "xhigh" || projection.Provider != "anthropic" {
		t.Fatalf("投影档位字段不符：%+v", projection)
	}
	if projection.Source != string(agentconfig.RoutingSourceSession) {
		t.Fatalf("来源 = %q，期望 session", projection.Source)
	}
	if !strings.Contains(projection.Revision, "tui") {
		t.Fatalf("revision 应包含写入端：%q", projection.Revision)
	}
	if projection.EffectiveFrom != agentconfig.RoutingEffectiveFromNextTurn {
		t.Fatalf("effective_from = %q，期望 next_turn", projection.EffectiveFrom)
	}
}

// TestChatRoutingStatusSegmentConfigSourceHasNoSuffix 钉住来源后缀规则：
// config 层无后缀（只有 session=* / workspace=~ 才有）。
func TestChatRoutingStatusSegmentConfigSourceHasNoSuffix(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"easy", "normal", "hard"},
				DefaultDifficulty: "normal",
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"normal": {Provider: "deepseek", Model: "deepseek-v3", ReasoningEffort: "medium"},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)

	segment := chatSurfaceRoutingStatusSegment(session)
	if segment.full != "route:normal · deepseek-v3 · medium" {
		t.Fatalf("config 来源不应带后缀，得到 %q", segment.full)
	}
	projection := chatRoutingStatusProjection(session)
	if projection.Source != string(agentconfig.RoutingSourceConfig) {
		t.Fatalf("来源 = %q，期望 config", projection.Source)
	}
	if projection.Revision != "" {
		t.Fatalf("无会话覆盖时 revision 应为空，得到 %q", projection.Revision)
	}
}

// TestChatRoutingStatusSegmentKeepsBaselineWithoutProfile 钉住兜底语义：
// 档位已判定但该档位无 profile 时，仍显示 route:<level>（不吞段）。
func TestChatRoutingStatusSegmentKeepsBaselineWithoutProfile(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"easy", "normal"},
				DefaultDifficulty: "easy",
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)
	if got := chatSurfaceRoutingStatusSegment(session).full; got != "route:easy" {
		t.Fatalf("无 profile 的档位应只显示 route:<level>，得到 %q", got)
	}
}

// TestChatStatusSegmentOrderPlacesRoutingAfterProvider 钉住 B6 段序：
// model → provider → routing（balance 由 chatAccountBalanceStatusInsertIndex 落点）。
func TestChatStatusSegmentOrderPlacesRoutingAfterProvider(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"hard"},
				DefaultDifficulty: "hard",
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"hard": {Model: "claude-opus-4"},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)
	session.Model = "baseline-model"
	session.ProviderName = "anthropic"

	segments := buildChatSurfaceStatusSegments(session, chatSurfaceStatus{kind: chatSurfaceStatusIdle}, chatInputModeChat)
	index := map[style.StatusSegmentKind]int{}
	for position, segment := range segments {
		if _, seen := index[segment.kind]; !seen {
			index[segment.kind] = position
		}
	}
	routingAt, ok := index[style.StatusSegRouting]
	if !ok {
		t.Fatalf("状态栏必须包含 routing 段：%#v", segments)
	}
	if modelAt, ok := index[style.StatusSegModel]; ok && modelAt > routingAt {
		t.Fatalf("routing 段必须排在 model 之后：%#v", index)
	}
	if providerAt, ok := index[style.StatusSegProvider]; ok && providerAt > routingAt {
		t.Fatalf("routing 段必须排在 provider 之后：%#v", index)
	}
}

// TestChatAccountBalanceStatusInsertIndexPrefersRoutingAnchor 覆盖 B6 索引修订：
// balance 插入锚点优先 routing 之后，其次 provider，最后 model。
func TestChatAccountBalanceStatusInsertIndexPrefersRoutingAnchor(t *testing.T) {
	withRouting := []style.StatusSegment{
		{Kind: style.StatusSegModel},
		{Kind: style.StatusSegProvider},
		{Kind: style.StatusSegRouting},
		{Kind: style.StatusSegUsage},
	}
	if got := chatAccountBalanceStatusInsertIndex(withRouting); got != 3 {
		t.Fatalf("routing 之后应为 3，得到 %d", got)
	}
	withoutRouting := []style.StatusSegment{
		{Kind: style.StatusSegModel},
		{Kind: style.StatusSegProvider},
		{Kind: style.StatusSegUsage},
	}
	if got := chatAccountBalanceStatusInsertIndex(withoutRouting); got != 2 {
		t.Fatalf("无 routing 段时锚点应回到 provider 之后（2），得到 %d", got)
	}
	modelOnly := []style.StatusSegment{
		{Kind: style.StatusSegModel},
		{Kind: style.StatusSegUsage},
	}
	if got := chatAccountBalanceStatusInsertIndex(modelOnly); got != 1 {
		t.Fatalf("无 provider/routing 段时锚点应回到 model 之后（1），得到 %d", got)
	}
}

// TestChatRoutingLevelSummariesOrdersBuiltinLevels 钉住面板逐级表格顺序：
// 内置四档优先，其余字典序追加（§5.3/§5.7 面板数据源）。
func TestChatRoutingLevelSummariesOrdersBuiltinLevels(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"zeta", "hard", "easy"},
				DefaultDifficulty: "easy",
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"easy": {Model: "cheap-model"},
					"hard": {Model: "strong-model"},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)

	rows := chatRoutingLevelSummaries(session, "main")
	if len(rows) != 3 {
		t.Fatalf("应产出 3 行档位，得到 %#v", rows)
	}
	if rows[0].Level != "easy" || rows[1].Level != "hard" || rows[2].Level != "zeta" {
		t.Fatalf("档位顺序应为 easy → hard → zeta，得到 %#v", rows)
	}
	if rows[1].Model != "strong-model" {
		t.Fatalf("hard 行 model 不符：%#v", rows[1])
	}
}

// TestChatStatusFitKeepsModelAndRoutingOnNarrowTerminal 覆盖 §10.2 I-7：窄终端
// 裁剪只丢尾部可选段，model 与 routing 段必须保留（段序 model → provider →
// routing 由 B6 钉住，裁剪按尾部丢弃，二者都在前部因此不会先被丢）。
func TestChatStatusFitKeepsModelAndRoutingOnNarrowTerminal(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"hard"},
				DefaultDifficulty: "hard",
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"hard": {Model: "claude-opus-4", ReasoningEffort: "xhigh"},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)
	session.Model = "baseline-model"
	session.ProviderName = "anthropic"

	segments := buildChatSurfaceStatusSegments(session, chatSurfaceStatus{kind: chatSurfaceStatusIdle}, chatInputModeChat)
	routingAt := -1
	for position, segment := range segments {
		if segment.kind == style.StatusSegRouting {
			routingAt = position
			break
		}
	}
	if routingAt < 0 {
		t.Fatalf("状态栏必须包含 routing 段：%#v", segments)
	}

	// 目标宽度：恰好容纳「routing 段及其之前」的 compact 形态，之后的段必须被裁掉。
	parts := make([]string, routingAt+1)
	for i := 0; i <= routingAt; i++ {
		parts[i] = segments[i].compact
		if parts[i] == "" {
			parts[i] = segments[i].full
		}
	}
	width := ui.DisplayWidth(strings.Join(parts, chatSurfaceStatusSeparator))

	fitted := fitChatSurfaceStatusSegments(segments, width)
	kinds := map[style.StatusSegmentKind]bool{}
	for _, segment := range fitted {
		kinds[segment.kind] = true
	}
	if !kinds[style.StatusSegModel] {
		t.Fatalf("窄终端裁剪必须保留 model 段：width=%d fitted=%+v", width, fitted)
	}
	if !kinds[style.StatusSegRouting] {
		t.Fatalf("窄终端裁剪必须保留 routing 段：width=%d fitted=%+v", width, fitted)
	}
	if len(fitted) > routingAt+1 {
		t.Fatalf("宽度不足时不应保留 routing 之后的可选段：width=%d fitted=%+v", width, fitted)
	}
	texts := make([]string, 0, len(fitted))
	for _, segment := range fitted {
		texts = append(texts, segment.text)
	}
	if line := strings.Join(texts, chatSurfaceStatusSeparator); ui.DisplayWidth(line) > width {
		t.Fatalf("裁剪结果仍超宽：%q (%d > %d)", line, ui.DisplayWidth(line), width)
	}
}
