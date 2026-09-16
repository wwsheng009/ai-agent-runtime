package events_test

import (
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
// 落点——contract.ChatSSEEventPrefix 必须与 api/skills 侧下帧用的 chatSSEStreamEventPrefix
// 同值，否则前端按生成物解析帧名、后端按自家常量下帧，两边会静默错位。
func TestChatSSEPrefixMatchesSkillsConstant(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "api", "skills", "trajectory_events.go")
	value, ok := namedStringConstInFile(t, path, "chatSSEStreamEventPrefix")
	if !ok {
		t.Fatalf("%s 中未找到字符串常量 chatSSEStreamEventPrefix（被改名或删除？）", path)
	}
	if value != events.ChatSSEEventPrefix {
		t.Fatalf("chat SSE 前缀漂移：api/skills=%q，contract=%q", value, events.ChatSSEEventPrefix)
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
