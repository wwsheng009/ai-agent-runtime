package events_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// 事件契约注册表的门禁（方案 §4 Batch 2）。
//
// 三条不变量：
//  1. 注册表里的每个类型都必须是 runtimeobserve 的「产品已知类型」——防拼写错误与
//     凭空登记（反过来，已知目录里的 chat 侧产品事件也必须登记，见第 2 条）；
//  2. internal/chat/events.go 声明的每个事件常量都必须登记（AST 解析源码，不维护
//     第二份手写清单）——新增事件常量未登记即失败；
//  3. contract.go 不得 import 任何非标准库包——chat → events 已是既有依赖方向，
//     反向 import 会构成 cycle，必须由结构断言挡住。

// repoRoot 从测试文件所在目录向上找到 go.mod，返回仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// TestChatSSEPrefixMatchesSkillsConstant：chat SSE 前缀是「字面量 + 门禁」策略的第二处
// 落点——contract.ChatSSEEventPrefix 必须与 api/runtimeapi 侧下帧用的 chatSSEStreamEventPrefix
// 同值，否则前端按生成物解析帧名、后端按自家常量下帧，两边会静默错位。
func TestChatSSEPrefixMatchesSkillsConstant(t *testing.T) {
	// 2026-09：internal/api/skills 已重命名为 internal/api/runtimeapi（提交 8e3744c2），
	// 本门禁的取样路径同步跟进——否则门禁会因路径不存在而恒红，失去把关作用。
	path := filepath.Join(repoRoot(t), "internal", "api", "runtimeapi", "trajectory_events.go")
	value, ok := namedStringConstInFile(t, path, "chatSSEStreamEventPrefix")
	if !ok {
		t.Fatalf("%s 中未找到字符串常量 chatSSEStreamEventPrefix（被改名或删除？）", path)
	}
	if value != events.ChatSSEEventPrefix {
		t.Fatalf("chat SSE 前缀漂移：api/runtimeapi=%q，contract=%q", value, events.ChatSSEEventPrefix)
	}
}

// namedStringConstInFile 取指定名字的顶层 const 字符串字面量值。
func namedStringConstInFile(t *testing.T, path, name string) (string, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range valueSpec.Names {
				if ident.Name != name || i >= len(valueSpec.Values) {
					continue
				}
				lit, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				return unquoted, true
			}
		}
	}
	return "", false
}

// stringConstsInFile 解析 Go 源文件，返回其中全部顶层 const 字符串字面量。
func stringConstsInFile(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, value := range valueSpec.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				out = append(out, unquoted)
			}
		}
	}
	return out
}

// TestRegisteredTypesAreKnownToObserveCatalog：方向 1（注册表 → 已知目录）。
func TestRegisteredTypesAreKnownToObserveCatalog(t *testing.T) {
	registered := events.RegisteredEventTypes()
	if len(registered) == 0 {
		t.Fatal("注册表为空")
	}
	var unknown []string
	for _, eventType := range registered {
		if !runtimeobserve.IsKnownEventType(eventType) {
			unknown = append(unknown, eventType)
		}
	}
	if len(unknown) > 0 {
		t.Fatalf("以下注册类型不在 runtimeobserve 已知类型目录内（补齐目录或删除登记）：%v", unknown)
	}
}

// TestChatEventConstantsAreRegistered：方向 2（chat 事件常量 → 注册表）。
func TestChatEventConstantsAreRegistered(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "chat", "events.go")
	consts := stringConstsInFile(t, path)
	if len(consts) < 30 {
		t.Fatalf("internal/chat/events.go 解析出的常量数异常：%d", len(consts))
	}
	var missing []string
	for _, eventType := range consts {
		if !events.IsRegisteredEventType(eventType) {
			missing = append(missing, eventType)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("internal/chat/events.go 中未登记进契约注册表的类型：%v", missing)
	}
}

// TestExternalEventFamilyConstantsAreRegistered：其余事件家族的具名常量。
func TestExternalEventFamilyConstantsAreRegistered(t *testing.T) {
	cases := []struct {
		eventType string
		channel   events.ChannelSet
	}{
		{toolprotocol.EventTypeProgress, events.ChannelLiveOnly},
		{supervision.EventTypeSubagentProgress, events.ChannelLiveOnly},
		{agentcontrol.EventAgentReclaimed, events.ChannelSessionStore},
		{chat.EventAssistantDelta, events.ChannelSessionStore},
		{chat.EventAssistantReasoningDelta, events.ChannelSessionStore},
		{chat.EventSessionCompactFailed, events.ChannelSessionStore},
		// 托管 turn 生命周期里程碑（方案 §6.8 / 审计 G3）：A+D，边沿触发。
		// 与 TestSubagentAuditEventChannelRegistrations 同口径地断言"已知目录"三
		// 方一致，避免只改注册表忘了 runtimeobserve 目录。
		{events.EventTurnSuspended, events.ChannelSessionStore | events.ChannelTailOnly},
		{events.EventTurnResumed, events.ChannelSessionStore | events.ChannelTailOnly},
	}
	for _, tc := range cases {
		if !events.IsRegisteredEventType(tc.eventType) {
			t.Errorf("%q 未登记", tc.eventType)
			continue
		}
		if got := events.ChannelsFor(tc.eventType); got != tc.channel {
			t.Errorf("%q 通道 = %d，期望 %d", tc.eventType, got, tc.channel)
		}
		if !runtimeobserve.IsKnownEventType(tc.eventType) {
			t.Errorf("%q 不在 runtimeobserve 已知类型目录内（三分法会把它当未知类型）", tc.eventType)
		}
	}
}

// TestSubagentTaskEventChannelRegistrations：M4（P0-1b）登记的 subagent 任务事件通道。
// 只补登记不改行为：高频批次进度走 live-only，任务里程碑与 batch.started/completed
// 同档走 tail-only。
func TestSubagentTaskEventChannelRegistrations(t *testing.T) {
	cases := []struct {
		eventType string
		channel   events.ChannelSet
	}{
		{"subagent.batch.progress", events.ChannelLiveOnly},
		{"subagent.task.started", events.ChannelTailOnly},
		{"subagent.task.completed", events.ChannelTailOnly},
	}
	for _, tc := range cases {
		if !events.IsRegisteredEventType(tc.eventType) {
			t.Errorf("%q 未登记", tc.eventType)
			continue
		}
		if got := events.ChannelsFor(tc.eventType); got != tc.channel {
			t.Errorf("%q 通道 = %d，期望 %d", tc.eventType, got, tc.channel)
		}
	}

	// live-only 快照列表（sessionLiveOnlyRuntimeEventTypes 的派生来源）必须包含
	// subagent.batch.progress，否则实时旁路仍会把它当未知类型裁掉。
	found := false
	for _, eventType := range events.LiveOnlyEventTypes() {
		if eventType == "subagent.batch.progress" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("LiveOnlyEventTypes 缺少 %q", "subagent.batch.progress")
	}
}

// TestSubagentAuditEventChannelRegistrations：SA-G1/SA-G2 登记的子代理审计事件通道。
// subagent.route.resolved 与 4 个批次失败态终态走 A+D：失败态需要事后追责与统计，
// 而成功态有 subagent.completed 与批次账本兜底（方案 §5.2 第一步）。
// 三方一致（常量 ↔ 注册表 ↔ 已知目录）在同一处断言。
func TestSubagentAuditEventChannelRegistrations(t *testing.T) {
	dualChannel := events.ChannelSessionStore | events.ChannelTailOnly
	cases := []struct {
		name      string
		eventType string
		channel   events.ChannelSet
	}{
		{"route_resolved", events.EventSubagentRouteResolved, dualChannel},
		{"batch_failed", events.EventSubagentBatchFailed, dualChannel},
		{"batch_canceled", events.EventSubagentBatchCanceled, dualChannel},
		{"batch_timed_out", events.EventSubagentBatchTimedOut, dualChannel},
		{"batch_orphaned", events.EventSubagentBatchOrphaned, dualChannel},
		{"suspension_unavailable", events.EventSubagentSuspensionUnavailable, dualChannel},
	}
	for _, tc := range cases {
		if strings.TrimSpace(tc.eventType) == "" || tc.eventType != strings.TrimSpace(tc.eventType) {
			t.Errorf("%s 的常量值非法：%q", tc.name, tc.eventType)
			continue
		}
		if !events.IsRegisteredEventType(tc.eventType) {
			t.Errorf("%s（%q）未登记进 runtimeEventContracts", tc.name, tc.eventType)
			continue
		}
		if got := events.ChannelsFor(tc.eventType); got != tc.channel {
			t.Errorf("%s（%q）通道 = %d，期望 %d", tc.name, tc.eventType, got, tc.channel)
		}
		if !runtimeobserve.IsKnownEventType(tc.eventType) {
			t.Errorf("%s（%q）不在 runtimeobserve 已知类型目录内（三分法会把它当未知类型）", tc.name, tc.eventType)
		}
	}
}

// TestEmitRuntimeEventLiteralsAreRegistered：SA-G2 的扫描型门禁（方案 §5.2 第二步
// 的辅措施）。病因是「门禁覆盖不到 internal/agent 里的裸字面量」：只要事件名以
// 字面量散落在发射点，注册表与发射点之间就没有任何机械约束，漏登记的症状只是
// 「前端没反应 / 事后查不到账」。
//
// 扫描 internal/agent、internal/api/runtimeapi、internal/toolbroker 的生产源码
// （跳过 _test.go），对 emitRuntimeEvent("<literal>" 形态的调用提取字面量并断言
// 已登记。登记为 0 通道也是合法表态（「当前无 chat 侧交付通道」），因此本门禁
// 只强迫**表态**，不强迫给通道；新发射点更推荐改用 internal/events 的常量。
func TestEmitRuntimeEventLiteralsAreRegistered(t *testing.T) {
	root := repoRoot(t)
	type site struct {
		file string
		line int
		typ  string
	}
	var sites []site
	fileCount := 0
	for _, dir := range []string{"agent", "api/runtimeapi", "toolbroker"} {
		base := filepath.Join(root, "internal", filepath.FromSlash(dir))
		walkErr := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fileCount++
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel == nil || selector.Sel.Name != "emitRuntimeEvent" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, unquoteErr := strconv.Unquote(lit.Value)
				if unquoteErr != nil {
					t.Errorf("unquote %s: %v", lit.Value, unquoteErr)
					return true
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				sites = append(sites, site{file: rel, line: fset.Position(lit.Pos()).Line, typ: value})
				return true
			})
			return nil
		})
		if walkErr != nil {
			t.Fatalf("扫描 %s 失败：%v", base, walkErr)
		}
	}
	if fileCount == 0 {
		t.Fatal("扫描范围为空：目录被改名或移动？")
	}
	if len(sites) == 0 {
		t.Fatal("未扫描到任何 emitRuntimeEvent 字面量调用：解析逻辑失效？")
	}
	var violations []string
	for _, item := range sites {
		if !events.IsRegisteredEventType(item.typ) {
			violations = append(violations, fmt.Sprintf("%s:%d → %q", item.file, item.line, item.typ))
		}
	}
	if len(violations) > 0 {
		t.Fatalf("以下 emitRuntimeEvent 字面量未登记进契约注册表（登记为 0 通道也是合法表态，"+
			"或在发射点改用 internal/events 的常量）：\n%s", strings.Join(violations, "\n"))
	}
}

// TestContractFileImportsOnlyStdlib：contract.go 的结构约束（防 import cycle）。
func TestContractFileImportsOnlyStdlib(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "events", "contract.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquote import: %v", err)
		}
		if strings.ContainsAny(importPath, "./") {
			t.Fatalf("contract.go 不得 import 非标准库包（会与 internal/chat 构成 cycle）：%s", importPath)
		}
	}
}

// TestContractRegistryInvariants：注册表自身的结构不变量。
func TestContractRegistryInvariants(t *testing.T) {
	seen := make(map[string]bool)
	for _, eventType := range events.RegisteredEventTypes() {
		if eventType == "" || eventType != strings.TrimSpace(eventType) {
			t.Fatalf("类型名非法（空或含首尾空白）：%q", eventType)
		}
		if seen[eventType] {
			t.Fatalf("注册表存在重复类型：%q", eventType)
		}
		seen[eventType] = true

		channels := events.ChannelsFor(eventType)
		if channels&events.ChannelLiveOnly != 0 && channels&events.ChannelSessionStore != 0 {
			t.Fatalf("%q 同时声明 live_only 与 session_store：B 通道语义是「仅实时、不落盘」", eventType)
		}
	}

	// 通道名是快照契约（runtime_event_delivery 的 channels 数组与前端生成物共用）。
	names := map[string]string{
		events.ChannelNameSessionStore: "session_store",
		events.ChannelNameLiveOnly:     "live_only",
		events.ChannelNameChatBridge:   "chat_bridge",
		events.ChannelNameTailOnly:     "tail_only",
	}
	for got, want := range names {
		if got != want {
			t.Fatalf("通道名契约被改动：got %q want %q", got, want)
		}
	}
}

// TestLiveOnlyTypesMatchChannelSet：LiveOnlyEventTypes 与 B 通道位一致。
func TestLiveOnlyTypesMatchChannelSet(t *testing.T) {
	liveOnly := events.LiveOnlyEventTypes()
	if len(liveOnly) == 0 {
		t.Fatal("live-only 列表为空")
	}
	for _, eventType := range liveOnly {
		if !events.IsLiveOnlyEventType(eventType) {
			t.Errorf("%q 出现在 LiveOnlyEventTypes 但不带 B 通道位", eventType)
		}
	}
	all := events.TypesWithChannel(events.ChannelLiveOnly)
	if strings.Join(all, ",") != strings.Join(liveOnly, ",") {
		t.Errorf("LiveOnlyEventTypes 与 TypesWithChannel(ChannelLiveOnly) 不一致：%v vs %v", liveOnly, all)
	}
}

// TestMainAgentRouteEventChannelRegistrations：MA-P4（MG3 修复）登记的主 Agent 动态
// route 治理事件通道。全部走 A 通道（成本归因/失败路径/还原证据必须 durable），
// 其中成本护栏触发是关键事件（PersistCritical ⇒ 落盘桥接立即 flush）。
func TestMainAgentRouteEventChannelRegistrations(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		channel   events.ChannelSet
	}{
		{"route_applied", events.EventMainAgentRouteApplied, events.ChannelSessionStore},
		{"route_prediction_invalid", events.EventMainAgentRoutePredictionInvalid, events.ChannelSessionStore},
		{"route_prediction_unresolvable", events.EventMainAgentRoutePredictionUnresolvable, events.ChannelSessionStore},
		{"route_disabled_for_turn", events.EventMainAgentRouteDisabledForTurn, events.ChannelSessionStore},
		{"route_cost_guard_tripped", events.EventMainAgentRouteCostGuardTripped, events.ChannelSessionStore},
		{"route_cleared", events.EventMainAgentRouteCleared, events.ChannelSessionStore},
		// 在途健康门禁的既泄漏信号（§6.2 第 4 步）：补登记后它们必须可落盘，
		// 否则主 Agent 的健康降级在审计中完全无痕。
		{"provider_health_opened", events.EventLLMProviderHealthOpened, events.ChannelSessionStore},
		{"prompt_cache_breaker_tripped", events.EventLLMPromptCacheBreakerTripped, events.ChannelSessionStore},
	}
	for _, tc := range cases {
		if !events.IsRegisteredEventType(tc.eventType) {
			t.Errorf("%s（%q）未登记", tc.name, tc.eventType)
			continue
		}
		if got := events.ChannelsFor(tc.eventType); got != tc.channel {
			t.Errorf("%s（%q）通道 = %d，期望 %d", tc.name, tc.eventType, got, tc.channel)
		}
	}

	if !events.IsPersistCriticalEventType(events.EventMainAgentRouteCostGuardTripped) {
		t.Errorf("%q 必须是 PersistCritical：成本护栏触发要压缩崩溃窗口",
			events.EventMainAgentRouteCostGuardTripped)
	}
	// 其余 route 事件不是关键事件：每 turn 1–3 行的常态留痕不应打断批量落盘。
	for _, eventType := range []string{
		events.EventMainAgentRouteApplied,
		events.EventMainAgentRoutePredictionInvalid,
		events.EventMainAgentRoutePredictionUnresolvable,
		events.EventMainAgentRouteDisabledForTurn,
		events.EventMainAgentRouteCleared,
	} {
		if events.IsPersistCriticalEventType(eventType) {
			t.Errorf("%q 不应是 PersistCritical", eventType)
		}
	}
}

// mainAgentEventFamily 返回「主 Agent route 治理 + 健康门禁」事件家族的
// 常量名 → 类型值映射。三方一致门禁与裸字面量扫描共用这一份清单。
func mainAgentEventFamily() map[string]string {
	return map[string]string{
		"EventMainAgentRouteApplied":                events.EventMainAgentRouteApplied,
		"EventMainAgentRoutePredictionInvalid":      events.EventMainAgentRoutePredictionInvalid,
		"EventMainAgentRoutePredictionUnresolvable": events.EventMainAgentRoutePredictionUnresolvable,
		"EventMainAgentRouteDisabledForTurn":        events.EventMainAgentRouteDisabledForTurn,
		"EventMainAgentRouteCostGuardTripped":       events.EventMainAgentRouteCostGuardTripped,
		"EventMainAgentRouteCleared":                events.EventMainAgentRouteCleared,
		"EventLLMProviderHealthOpened":              events.EventLLMProviderHealthOpened,
		"EventLLMPromptCacheBreakerTripped":         events.EventLLMPromptCacheBreakerTripped,
	}
}

// TestMainAgentEventFamilyIsRegisteredAndKnown：三方一致（常量 ↔ 注册表 ↔ 已知目录）。
// 常量在 internal/events/main_agent_routing.go 定义一次，注册表与 runtimeobserve
// 目录必须同步；缺一处就会出现「发射了但不可见」或「三分法把它当未知」。
func TestMainAgentEventFamilyIsRegisteredAndKnown(t *testing.T) {
	for name, eventType := range mainAgentEventFamily() {
		if strings.TrimSpace(eventType) == "" || eventType != strings.TrimSpace(eventType) {
			t.Errorf("%s 的常量值非法：%q", name, eventType)
			continue
		}
		if !events.IsRegisteredEventType(eventType) {
			t.Errorf("%s（%q）未登记进 runtimeEventContracts", name, eventType)
		}
		if !runtimeobserve.IsKnownEventType(eventType) {
			t.Errorf("%s（%q）不在 runtimeobserve 已知类型目录内", name, eventType)
		}
	}
}

// TestMainAgentEventFamilyIsNotEmittedAsBareLiteral：§6.2 主措施的机械门禁
// （方案 §8.2 门禁 G-B 的「本方案自建等价扫描测试」分支）。
//
// 病因是「门禁覆盖不到 internal/agent 里的裸字面量」：只要事件名以字面量散落在
// 发射点，注册表与发射点之间就没有任何机械约束。本测试扫描 internal/ 下的**生产**
// 源码（跳过 _test.go），断言本家族的 8 个类型名只出现在两处**声明**位置：
//
//	internal/events/main_agent_routing.go   （常量定义）
//	internal/runtimeobserve/known_types.go  （产品已知类型目录）
//
// 其余任何出现（尤其 emitRuntimeEvent("<literal>")）即失败——发射点必须引用常量。
//
// 范围说明：本门禁**只覆盖本方案的事件家族**。internal/agent 里其余历史裸字面量
// 的收编属姊妹方案 SA-G2（扫描型测试）的范围，此处不越界——把它们的存量违规
// 一并纳入会让本测试一上线即红，从而被迫做一批与本方案无关的登记。
func TestMainAgentEventFamilyIsNotEmittedAsBareLiteral(t *testing.T) {
	root := repoRoot(t)
	family := mainAgentEventFamily()
	familyValues := make(map[string]bool, len(family))
	for _, eventType := range family {
		familyValues[eventType] = true
	}
	exempt := map[string]bool{
		filepath.Join(root, "internal", "events", "main_agent_routing.go"):  true,
		filepath.Join(root, "internal", "runtimeobserve", "known_types.go"): true,
	}

	var violations []string
	walkErr := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if exempt[path] {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if familyValues[unquoted] {
				position := fset.Position(lit.Pos())
				rel, relErr := filepath.Rel(root, position.Filename)
				if relErr != nil {
					rel = position.Filename
				}
				violations = append(violations, rel+":"+strconv.Itoa(position.Line)+" "+unquoted)
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描 internal/ 失败：%v", walkErr)
	}
	if len(violations) > 0 {
		t.Fatalf("本家族事件名不得以裸字面量出现（改用 internal/events 的常量）：%v", violations)
	}
}
