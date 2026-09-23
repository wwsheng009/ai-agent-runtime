package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 方案 §5.3/§5.3.1：会话级路由面板（Level → 字段 → 值 三级导航）单测。
// 交互路径本身依赖真实 TTY（ui.SelectFullScreenListWithLease），因此这里钉住
// 可判定的部分：写入键映射、键路径直达解析、建议值集合、列表项渲染，以及
// 「无全屏能力时退化为行内文本且不落盘」的边界（I-1/I-11）。

func TestChatRoutingPanelWriteKey(t *testing.T) {
	cases := []struct {
		scope string
		level string
		field string
		want  string
	}{
		{"main", "hard", "model", "profiles.hard.model"},
		{"sub", "expert", "reasoning_effort", "levels.expert.reasoning_effort"},
		{"main", "hard", "prompt_cache", "profiles.hard.prompt_cache"},
		{"main", "hard", "enabled", "enabled"},
		{"main", "hard", "profiles.normal.model", "profiles.normal.model"},
		{"sub", "expert", "levels.normal.provider", "levels.normal.provider"},
		{"main", "hard", "", ""},
	}
	for _, tc := range cases {
		if got := chatRoutingPanelWriteKey(tc.scope, tc.level, tc.field); got != tc.want {
			t.Fatalf("writeKey(%q,%q,%q) = %q，期望 %q", tc.scope, tc.level, tc.field, got, tc.want)
		}
	}
}

func TestChatRoutingPanelParseKeyPath(t *testing.T) {
	cases := []struct {
		level   string
		keyPath string
		wantLvl string
		wantKey string
	}{
		{"", "profiles.hard.model", "hard", "model"},
		{"", "levels.expert.temperature", "expert", "temperature"},
		{"hard", "model", "hard", "model"},
		{"easy", "profiles.expert.temperature", "expert", "temperature"},
		{"hard", "bogus.deep.path", "hard", "bogus.deep.path"},
	}
	for _, tc := range cases {
		level, key := chatRoutingPanelParseKeyPath(tc.level, tc.keyPath)
		if level != tc.wantLvl || key != tc.wantKey {
			t.Fatalf("parseKeyPath(%q,%q) = (%q,%q)，期望 (%q,%q)", tc.level, tc.keyPath, level, key, tc.wantLvl, tc.wantKey)
		}
	}
}

func TestChatRoutingPanelValueOptionsStaticFields(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	for _, field := range []string{"enabled", "prompt_cache"} {
		options := chatRoutingPanelValueOptions(session, field)
		if len(options) != 2 || options[0] != "on" || options[1] != "off" {
			t.Fatalf("%s 候选应为 on/off，得到 %v", field, options)
		}
	}
	// 数值/自由文本字段没有目录候选：面板给「直接写入」提示而不是伪造建议值。
	for _, field := range []string{"max_tokens", "temperature", "thinking_effort", "unknown"} {
		if options := chatRoutingPanelValueOptions(session, field); len(options) != 0 {
			t.Fatalf("%s 不应有候选，得到 %v", field, options)
		}
	}
}

func TestChatRoutingPanelValueItemsMarksCurrentAndPreview(t *testing.T) {
	options := []string{"claude-opus-4", "claude-sonnet-4"}
	items := chatRoutingPanelValueItems(options, "main", "hard", "model", "claude-opus-4")
	if len(items) != 2 {
		t.Fatalf("值列表项数不符：%d", len(items))
	}
	if items[0].Title != "claude-opus-4  (当前)" {
		t.Fatalf("当前值应带 (当前) 标记：%q", items[0].Title)
	}
	if items[1].Title != "claude-sonnet-4" {
		t.Fatalf("非当前值不应带标记：%q", items[1].Title)
	}
	if !strings.Contains(items[1].Preview, "profiles.hard.model = claude-sonnet-4") ||
		!strings.Contains(items[1].Preview, "下一 turn 生效") {
		t.Fatalf("预览应显示写入键与生效时机：%q", items[1].Preview)
	}
	// 写回值按原始候选下标取（Title 上的 (当前) 标记不得进入写入值）。
	if options[0] != "claude-opus-4" {
		t.Fatalf("下标与候选序列必须一一对应：%v", options)
	}
}

func TestChatRoutingPanelLevelItemsAlwaysOfferBuiltinLevels(t *testing.T) {
	empty := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	items := chatRoutingPanelLevelItems(empty, "main")
	if len(items) != len(chatRoutingPanelBuiltinLevels) {
		t.Fatalf("零配置时应给出内置四档，得到 %d 项", len(items))
	}
	for index, level := range chatRoutingPanelBuiltinLevels {
		if items[index].Title != level || !strings.Contains(items[index].Detail, "未配置") {
			t.Fatalf("内置档位项不符：%+v", items[index])
		}
	}

	configured := routingCommandSessionWithOverride(t)
	items = chatRoutingPanelLevelItems(configured, "main")
	if len(items) != 4 {
		t.Fatalf("配置 easy/normal/hard 时应为内置四档，得到 %d 项：%+v", len(items), items)
	}
	hard := chatRoutingPanelLevelItems(configured, "main")[2]
	if hard.Title != "hard" {
		t.Fatalf("档位顺序应内置优先：%+v", items)
	}
	for _, want := range []string{"anthropic", "claude-opus-4", "xhigh", "(session)"} {
		if !strings.Contains(hard.Detail, want) {
			t.Fatalf("hard 行缺少 %q：%q", want, hard.Detail)
		}
	}
	expert := chatRoutingPanelLevelItems(configured, "main")[3]
	if expert.Title != "expert" || !strings.Contains(expert.Detail, "未配置") {
		t.Fatalf("未配置档位应标注未配置：%+v", expert)
	}
}

func TestChatRoutingPanelFieldItemsCoverWritableKeySpace(t *testing.T) {
	items := chatRoutingPanelFieldItems()
	keys := make(map[string]struct{}, len(items))
	for _, item := range items {
		keys[item.Title] = struct{}{}
	}
	for _, want := range []string{"enabled", "provider", "model", "reasoning_effort", "thinking_effort", "max_tokens", "temperature", "prompt_cache"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("字段列表缺少可写字段 %q：%+v", want, items)
		}
	}
}

// TestChatRoutingPanelEntryFallsBackToInlineText 钉住 §5.3.1 边界：无全屏能力
// （测试会话没有 Surface/Interaction）时，打开面板一族退化为只读摘要 + 提示，
// 且不产生任何写入（I-1）。
func TestChatRoutingPanelEntryFallsBackToInlineText(t *testing.T) {
	session := routingCommandSessionWithOverride(t)
	before := chatSessionRoutingOverrideRaw(session)

	cases := []struct {
		level   string
		wantHit string
	}{
		{"", "无法开启全屏面板"},
		{"hard", "面板可直达 hard 级"},
		{"profiles.hard.model", "面板可直达 hard 级"},
	}
	for _, tc := range cases {
		text := chatRoutingPanelEntry(session, "main", tc.level, "")
		if !strings.Contains(text, "路由（main）· 启用") || !strings.Contains(text, tc.wantHit) {
			t.Fatalf("入口 %q 退化输出不符（缺少 %q）：\n%s", tc.level, tc.wantHit, text)
		}
	}
	if after := chatSessionRoutingOverrideRaw(session); after != before {
		t.Fatalf("只读退化路径不得写入覆盖：before=%q after=%q", before, after)
	}
}

// TestChatRoutingCommandPanelEntryForms 钉住命令面三种「打开面板」入口（§5.2/
// §5.3.1）：裸 `/routing`、`/routing main|sub [<level>]`、键路径直达，均被接管；
// 无全屏能力时同为行内文本（不切换屏幕）。
func TestChatRoutingCommandPanelEntryForms(t *testing.T) {
	for _, command := range []string{"/routing", "/routing main", "/routing sub", "/routing main hard", "/routing main profiles.hard.model", "/routing sub levels.expert.model"} {
		session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
		text, handled := chatRoutingCommandText(session, command)
		if !handled {
			t.Fatalf("%s 必须被结构化入口接管", command)
		}
		if !strings.Contains(text, "无法开启全屏面板") {
			t.Fatalf("%s 在无全屏能力时应退化为行内文本：\n%s", command, text)
		}
		if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
			t.Fatalf("%s 不得产生写入：%q", command, raw)
		}
	}
}

// TestChatRoutingPanelLayerItemsMatchNames 钉住写入层选择器与层名的对应关系（§5.4/I-2）。
// 面板把用户选择的下标回查层名（chat_routing_panel.go：layerPicked →
// chatRoutingPanelLayerNames[layerPicked]，下标语义见 chat_picker_common.go:112-124
// 「original item index」），两列表顺序一旦漂移就会把值写进**错误的层**
// （例如用户选 config、实际落到 workspace），且没有编译期保护；同时选择项数量
// 必须恒为 3：即使工作区不可用也必须保留该档（Detail 说明不可用），否则下标整体错位。
func TestChatRoutingPanelLayerItemsMatchNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	bound := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	bound.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = t.TempDir()
	unbound := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	for name, session := range map[string]*ChatSession{"绑定工作区": bound, "未绑定工作区": unbound} {
		items := chatRoutingPanelLayerItems(session)
		if len(items) != len(chatRoutingPanelLayerNames) {
			t.Fatalf("%s: 选择项 %d 个与层名 %d 个不一致（下标回查会错位）", name, len(items), len(chatRoutingPanelLayerNames))
		}
		for i, want := range chatRoutingPanelLayerNames {
			if items[i].Title != want {
				t.Fatalf("%s: 第 %d 项 = %q，期望 %q", name, i, items[i].Title, want)
			}
			if strings.TrimSpace(items[i].SearchText) != want {
				t.Fatalf("%s: 第 %d 项 SearchText = %q，期望 %q", name, i, items[i].SearchText, want)
			}
		}
	}

	// config 档必须标注影响面并回显目标路径（选中即二次确认，§5.4）。
	configItem := chatRoutingPanelLayerItems(bound)[len(chatRoutingPanelLayerNames)-1]
	if !strings.Contains(configItem.Detail, "影响所有会话") {
		t.Fatalf("config 档应标注影响面：%q", configItem.Detail)
	}
	if path, kind := agentconfig.AICLIConfigWriteTargetForRouting(); strings.TrimSpace(path) != "" {
		if !strings.Contains(configItem.Detail, strings.TrimSpace(path)) {
			t.Fatalf("config 档应回显目标路径 %q：%q", path, configItem.Detail)
		}
		if !strings.Contains(configItem.Detail, strings.TrimSpace(kind)) {
			t.Fatalf("config 档应回显层类型 %q：%q", kind, configItem.Detail)
		}
	}
}

// TestChatRoutingPanelReasoningOptionsFollowLevelModel 钉住 §10.2 I-3：
// reasoning_effort 候选按「该级生效 model」过滤——档位写了 model 时给出该模型的
// 目录；档位只写 provider 时用其默认模型；档位没有 provider/model 时回落会话候选；
// 该级模型无能力卡片时给空候选（不回落会话候选，避免「不支持的值出现」）。
func TestChatRoutingPanelReasoningOptionsFollowLevelModel(t *testing.T) {
	override := &agentconfig.AICLISessionRoutingOverride{
		MainAgent: &agentconfig.AICLISessionMainAgentRoutingOverride{
			Enabled: routingTestBoolPtr(true),
			Levels:  routingTestStringsPtr("easy", "normal", "hard"),
			Profiles: map[string]agentconfig.AICLISessionRouteProfileOverride{
				"normal": {
					Provider: routingTestStringPtr("gamma"),
				},
				"hard": {
					Provider: routingTestStringPtr("beta"),
					Model:    routingTestStringPtr("beta-model"),
				},
			},
		},
	}
	session := routingStatusTestSession(t, override, &agentconfig.AICLIConfig{})
	session.Config.Providers = agentconfig.ProvidersConfig{Items: map[string]agentconfig.Provider{
		"alpha": {
			Enabled:      true,
			Protocol:     "openai",
			DefaultModel: "alpha-model",
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"alpha-model": {ReasoningEfforts: []string{"low", "medium"}},
			},
		},
		"beta": {
			Enabled:  true,
			Protocol: "anthropic",
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"beta-model": {ReasoningEfforts: []string{"high"}},
			},
		},
		"gamma": {
			Enabled:      true,
			Protocol:     "openai",
			DefaultModel: "gamma-model",
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"gamma-model": {ReasoningEfforts: []string{"minimal"}},
			},
		},
	}}
	session.ProviderName = "alpha"
	session.Provider = session.Config.Providers.Items["alpha"]
	session.Model = "alpha-model"

	sessionOptions := chatRoutingPanelValueOptions(session, "reasoning_effort")
	if len(sessionOptions) != 2 || sessionOptions[0] != "low" || sessionOptions[1] != "medium" {
		t.Fatalf("会话 reasoning 候选应为 alpha 目录，得到 %v", sessionOptions)
	}

	hard := chatRoutingPanelValueOptionsForLevel(session, "main", "hard", "reasoning_effort")
	if len(hard) != 1 || hard[0] != "high" {
		t.Fatalf("hard 档位 reasoning 候选应来自该级 model（beta-model → high），得到 %v", hard)
	}

	normal := chatRoutingPanelValueOptionsForLevel(session, "main", "normal", "reasoning_effort")
	if len(normal) != 1 || normal[0] != "minimal" {
		t.Fatalf("只覆盖 provider 的档位应取该 provider 默认模型目录（gamma-model → minimal），得到 %v", normal)
	}

	easy := chatRoutingPanelValueOptionsForLevel(session, "main", "easy", "reasoning_effort")
	if strings.Join(easy, ",") != strings.Join(sessionOptions, ",") {
		t.Fatalf("未覆盖 model 的档位应回落会话候选，得到 %v（会话 %v）", easy, sessionOptions)
	}

	// provider 已注册但没有默认模型时无法确定该级模型：给空候选而不是会话候选。
	session.Config.Providers.Items["gamma"] = agentconfig.Provider{
		Enabled: true,
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gamma-model": {ReasoningEfforts: []string{"minimal"}},
		},
	}
	if got := chatRoutingPanelValueOptionsForLevel(session, "main", "normal", "reasoning_effort"); len(got) != 0 {
		t.Fatalf("无法确定该级模型时应给空候选，得到 %v", got)
	}

	keyPath := chatRoutingValueArgumentCandidates(session, "main", "hard", "profiles.hard.reasoning_effort")
	if !containsSlashCandidate(keyPath, "high") || containsSlashCandidate(keyPath, "low") {
		t.Fatalf("键路径补全应给该档位候选，得到 %#v", keyPath)
	}
	if derived := chatRoutingLevelFromKey("main", "profiles.hard.reasoning_effort"); derived != "hard" {
		t.Fatalf("chatRoutingLevelFromKey = %q，期望 hard", derived)
	}
}

// 方案 §5.3/§3.5：无目录候选的数值/文本字段（thinking_effort/max_tokens/temperature）
// 由面板自由文本录入。这里钉住两点：
//  1. 「可录入字段」是闭集——表外字段仍走「直接写入」提示，不开必然被拒的输入框；
//  2. 校验与写入路径同源（chatRoutingApplyKey + chatRoutingValidateCandidate），
//     预演拒绝的值写入也必然拒绝，预演通过的值写入必须成功（不会出现「面板能确认、
//     落盘却被拒」或反向的假入口）。
func TestChatRoutingPanelFreeTextFieldsUseWritePathValidation(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	for _, field := range []string{"thinking_effort", "max_tokens", "temperature"} {
		if !chatRoutingPanelFieldUsesFreeText(field) {
			t.Fatalf("%s 应支持自由文本录入", field)
		}
		if strings.TrimSpace(chatRoutingPanelFreeTextHint(field)) == "" {
			t.Fatalf("%s 应给出输入提示", field)
		}
	}
	for _, field := range []string{"model", "provider", "reasoning_effort", "enabled", "prompt_cache", "unknown", ""} {
		if chatRoutingPanelFieldUsesFreeText(field) {
			t.Fatalf("%s 不应走自由文本录入（有目录候选或不在可写键空间）", field)
		}
		if strings.TrimSpace(chatRoutingPanelFreeTextHint(field)) != "" {
			t.Fatalf("%s 不应给出自由文本提示", field)
		}
	}

	cases := []struct {
		name    string
		field   string
		value   string
		wantErr string // 空串表示必须通过
	}{
		{name: "max_tokens 正数", field: "max_tokens", value: "2048"},
		{name: "max_tokens 非数字", field: "max_tokens", value: "abc", wantErr: "max_tokens"},
		{name: "max_tokens 负数与档位校验同源", field: "max_tokens", value: "-1", wantErr: "max_tokens"},
		{name: "temperature 数字", field: "temperature", value: "0.2"},
		{name: "temperature 非数字", field: "temperature", value: "hot", wantErr: "temperature"},
		{name: "thinking_effort 文本", field: "thinking_effort", value: "high"},
		{name: "thinking_effort 空值", field: "thinking_effort", value: "   ", wantErr: "thinking_effort"},
		{name: "表外字段不得绕过字段解析", field: "bogus", value: "1", wantErr: "bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := chatRoutingPanelValidateValue(session, chatRoutingScopeMain, "hard", tc.field, tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过与写入同源的校验：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应被拒绝并回显字段 %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误应回显字段 %q：%v", tc.wantErr, err)
			}
		})
	}

	// 预演通过 → 真写入必须成功；预演拒绝 → 真写入必须同样拒绝。
	for field, value := range map[string]string{
		"max_tokens": "2048", "temperature": "0.2", "thinking_effort": "high",
	} {
		if err := chatRoutingPanelValidateValue(session, chatRoutingScopeMain, "hard", field, value); err != nil {
			t.Fatalf("预演应通过 %s=%s：%v", field, value, err)
		}
		key := chatRoutingPanelWriteKey(chatRoutingScopeMain, "hard", field)
		text, err := chatRoutingWriteKey(session, chatRoutingScopeMain, key, value, chatRoutingLayerSession, false)
		if err != nil {
			t.Fatalf("预演通过的值必须可写入 %s=%s：%v", field, value, err)
		}
		if !strings.Contains(text, "已写入会话路由覆盖") {
			t.Fatalf("写入回执不符：%q", text)
		}
	}
	// 面板预演与写入必须给出**同一文案**（同源不只是同结论）：负数 max_tokens 在
	// 解析器的回退阶梯下会把整层 enabled 丢回下层，两处都必须按字段回显真实原因
	// 并给出下一步（§10.2 I-4），不得一边报「路由未启用」一边报「数值非法」。
	previewErr := chatRoutingPanelValidateValue(session, chatRoutingScopeMain, "hard", "max_tokens", "-1")
	if previewErr == nil {
		t.Fatalf("负数 max_tokens 预演必须被拒")
	}
	_, writeErr := chatRoutingWriteKey(session, chatRoutingScopeMain, "profiles.hard.max_tokens", "-1", chatRoutingLayerSession, false)
	if writeErr == nil {
		t.Fatalf("负数 max_tokens 写入必须被拒（与面板预演一致）")
	}
	if previewErr.Error() != writeErr.Error() {
		t.Fatalf("面板预演与写入必须同源同文案：preview=%q write=%q", previewErr, writeErr)
	}
	if !strings.Contains(previewErr.Error(), "非负") {
		t.Fatalf("拒绝文案应给出下一步（I-4）：%v", previewErr)
	}
}

// ---------- §5.6/I-8/INV-A3（M16）：子会话只读全屏导航 ----------

// chatRoutingPanelChildSessionForTest 构造带子会话标记的会话：depth>0 与宿主接线
// （chat_actor_host.go 的 isBaseSession 分支）同源，命中 chatRoutingSessionIsChildAgent。
func chatRoutingPanelChildSessionForTest(t *testing.T) *ChatSession {
	t.Helper()
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	session.RuntimeSession.SetContext(toolbroker.AgentSessionContextDepth, 1)
	return session
}

// TestChatRoutingReadOnlyNavigationKeepsFieldSpace 钉住只读导航的字段闭集与行内信息：
// 只读视图必须覆盖与写入面板相同的字段（否则用户看不到真实键空间），行内给出当前
// 生效值/候选/取值说明，且每行都带上「写入不会生效」的原因（INV-A3）。
func TestChatRoutingReadOnlyNavigationKeepsFieldSpace(t *testing.T) {
	session := chatRoutingPanelChildSessionForTest(t)

	items := chatRoutingReadOnlyFieldItems(session, chatRoutingScopeMain, "hard")
	if len(items) != len(chatRoutingPanelFields) {
		t.Fatalf("只读字段数 = %d，期望与写入面板一致 = %d", len(items), len(chatRoutingPanelFields))
	}
	for i, item := range items {
		if want := chatRoutingPanelFields[i].Key; item.Title != want {
			t.Fatalf("第 %d 行 = %q，期望 %q（顺序与写入面板一致）", i, item.Title, want)
		}
		if !strings.Contains(item.Detail, "当前:") {
			t.Fatalf("%s 行内应带当前生效值：%q", item.Title, item.Detail)
		}
		if !strings.Contains(item.Preview, chatRoutingChildSessionReadOnlyNote) {
			t.Fatalf("%s 预览应说明写入不生效（INV-A3）：\n%s", item.Title, item.Preview)
		}
	}

	// 有目录候选的字段（enabled → on/off）行内直接给候选，不再多开一层无动作选择器。
	if !strings.Contains(items[0].Detail, "候选: off|on") {
		t.Fatalf("enabled 行内应给出候选：%q", items[0].Detail)
	}
	// 无目录候选的字段给取值说明（与写入面板同源），不伪造建议值（I-3）。
	fieldIndex := -1
	for i, item := range items {
		if item.Title == "max_tokens" {
			fieldIndex = i
			break
		}
	}
	if fieldIndex < 0 {
		t.Fatalf("只读字段缺少 max_tokens：%+v", items)
	}
	if !strings.Contains(items[fieldIndex].Detail, "非负整数") {
		t.Fatalf("max_tokens 行内应给出取值说明：%q", items[fieldIndex].Detail)
	}
}

// TestChatRoutingReadOnlyFieldSummaryStaysReadOnly 钉住只读摘要的终点语义：给出当前
// 生效值 + 候选/取值说明 + 写入键，并说明写入不生效；整个过程不写任何路由覆盖。
func TestChatRoutingReadOnlyFieldSummaryStaysReadOnly(t *testing.T) {
	session := chatRoutingPanelChildSessionForTest(t)
	before := chatSessionRoutingOverrideRaw(session)

	summary := chatRoutingReadOnlyFieldSummary(session, chatRoutingScopeMain, "hard", "provider")
	for _, want := range []string{"只读视图（子会话）", "当前生效值", "写入键: profiles.hard.provider", chatRoutingChildSessionReadOnlyNote} {
		if !strings.Contains(summary, want) {
			t.Fatalf("只读摘要缺少 %q：\n%s", want, summary)
		}
	}
	if !strings.Contains(summary, "未设置（继承下层）") {
		t.Fatalf("未覆盖字段应说明继承语义：\n%s", summary)
	}
	freeText := chatRoutingReadOnlyFieldSummary(session, chatRoutingScopeMain, "hard", "temperature")
	if !strings.Contains(freeText, "取值说明") {
		t.Fatalf("无目录候选字段应给取值说明而不是候选：\n%s", freeText)
	}
	// 只读承诺：行渲染与摘要都不得改写会话覆盖（INV-A5/§5.6）。
	if after := chatSessionRoutingOverrideRaw(session); after != before {
		t.Fatalf("只读导航改写了路由覆盖：%q → %q", before, after)
	}
}

// TestChatRoutingPanelEntryReadOnlyRoutingBySessionKind 钉住入口分流：子会话走只读
// 路径（无全屏能力时=文本摘要+原因，且与 chatRoutingPanelEntryText 拼接一致），
// 主会话不得被误标只读（INV-A3/M16）。
func TestChatRoutingPanelEntryReadOnlyRoutingBySessionKind(t *testing.T) {
	child := chatRoutingPanelChildSessionForTest(t)
	childEntry := chatRoutingPanelEntry(child, chatRoutingScopeMain, "", "")
	wantChild := chatRoutingPanelEntryText(child, chatRoutingScopeMain, "") + "\n" + chatRoutingChildSessionReadOnlyNote
	if childEntry != wantChild {
		t.Fatalf("子会话无全屏能力时应退化为「只读摘要 + 原因」：\n得到 %q\n期望 %q", childEntry, wantChild)
	}

	main := routingCommandSessionWithOverride(t)
	mainEntry := chatRoutingPanelEntry(main, chatRoutingScopeMain, "", "")
	if strings.Contains(mainEntry, chatRoutingChildSessionReadOnlyNote) {
		t.Fatalf("主会话入口不得出现子会话只读原因：\n%s", mainEntry)
	}
}
