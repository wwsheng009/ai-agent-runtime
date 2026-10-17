package toolbroker

// 对账测试（reconciliation contract）：把 toolregistry（内置工具身份单一真源）
// 与 broker 的命名契约焊在一起。
//
// 背景：此前 policy 与 broker 各持一份"规范名"，同一个工具因此出现两个规范名
// （background_task vs job_start、spawn_agent vs subagent_spawn、enter_plan_mode
// vs plan_enter）。本文件让任何一侧的单方面漂移在 CI 里立刻失败，而不是等到
// 运行期才表现为"allowlist 里的名字不再被识别为必需工具"。
//
// 之所以放在 toolbroker 而不是 toolregistry：toolregistry 是叶子包（只依赖
// 标准库），不能反向 import toolbroker。方向必须是 broker → registry。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// TestRegistryOrchestrationNamesMatchBrokerContract 正向对账：
//   - toolregistry 编排面工具的**规范名**必须是 broker normalizeToolName 的不动点；
//   - 别名必须归一到所属条目的规范名（旧拼写仍可调用，但不再是规范名）。
func TestRegistryOrchestrationNamesMatchBrokerContract(t *testing.T) {
	seen := 0
	var problems []string
	for _, entry := range toolregistry.Entries() {
		if entry.Group != toolregistry.GroupOrchestration {
			continue
		}
		seen++
		if got := normalizeToolName(entry.Name); got != entry.Name {
			problems = append(problems, fmt.Sprintf("registry canonical %q is not a broker normalizeToolName fixed point (got %q); use the broker naming-contract spelling", entry.Name, got))
		}
		for _, alias := range entry.Aliases {
			if got := normalizeToolName(alias); got != entry.Name {
				problems = append(problems, fmt.Sprintf("registry alias %q of %q must normalize onto %q, got %q", alias, entry.Name, entry.Name, got))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no orchestration entries found in toolregistry; the reconciliation test would silently pass")
	}
	if len(problems) > 0 {
		t.Fatalf("toolregistry and the broker naming contract disagree in %d place(s):\n  - %s",
			len(problems), strings.Join(problems, "\n  - "))
	}
}

// TestRegistryKnowsEveryBrokerCanonical 反向对账：broker 派发面的每个规范名都必须在
// toolregistry 里登记为**规范名**（而非别名）、且归 `orchestration` 组，否则"单一真源"存在盲区。
//
// 语料是**派生的**（遍历 `brokerToolCanonicalNames`）：此前这里手抄了 35 个常量，新增 broker
// 工具时会静默漏检（漏检即"broker 派发了一个注册表不认识/归错组的工具"）；现在新增工具只改
// broker 的表，本测试立刻覆盖。
func TestRegistryKnowsEveryBrokerCanonical(t *testing.T) {
	// 语料下限：防"表被清空"导致断言假绿（今天实测 43 个）。
	if len(brokerToolCanonicalNames) < 30 {
		t.Fatalf("broker canonical table has only %d entries; the reconciliation would silently pass", len(brokerToolCanonicalNames))
	}
	seen := make(map[string]bool, len(brokerToolCanonicalNames))
	for _, name := range brokerToolCanonicalNames {
		if seen[name] {
			t.Fatalf("broker canonical %q appears twice in brokerToolCanonicalNames", name)
		}
		seen[name] = true
		if got := toolregistry.Canonical(name); got != name {
			t.Fatalf("broker canonical %q resolves to %q in toolregistry; the registry must treat it as canonical, not as an alias", name, got)
		}
		entry, ok := toolregistry.Lookup(name)
		if !ok {
			t.Fatalf("broker canonical %q is not registered in toolregistry", name)
		}
		if entry.Group != toolregistry.GroupOrchestration {
			t.Fatalf("broker canonical %q is registered under group %q; broker-dispatched tools must be labelled %q", name, entry.Group, toolregistry.GroupOrchestration)
		}
	}
}

// TestRegistryLegacyAliasesAreNotCanonical 钉死"旧拼写不得再当规范名"。
//
// 这正是本次收敛要消灭的病：同一个能力出现两个规范名。
func TestRegistryLegacyAliasesAreNotCanonical(t *testing.T) {
	legacy := []string{
		"background_task", "task_output", "task_kill", "task_monitor", "task_control", "write_stdin", "task_input",
		"spawn_team", "wait_team", "send_team_message", "read_mailbox_digest",
		"read_task_spec", "read_task_context", "report_task_outcome", "block_current_task",
		"spawn_agent", "list_agents", "send_message", "followup_task", "send_input",
		"resolve_agent_approval", "wait_agent", "read_agent_events", "close_agent", "resume_agent",
		"apply_agent_worktree", "discard_agent_worktree",
		"enter_plan_mode", "exit_plan_mode",
	}
	for _, name := range legacy {
		got := toolregistry.Canonical(name)
		if got == name {
			t.Fatalf("legacy spelling %q is still its own canonical name in toolregistry; it must resolve to the contract spelling", name)
		}
		// 旧拼写必须仍可解析到某个已登记规范名（可调用性不回退）。
		if _, ok := toolregistry.Lookup(name); !ok {
			t.Fatalf("legacy spelling %q no longer resolves to a registered tool", name)
		}
	}
}

// TestRegistryResolvesEveryLegacySpelling 反向闭合：broker 命名契约钉死的每个
// legacy 拼写，都必须在 toolregistry 里解析到**同一个**规范名。
//
// 这堵的是本次收敛暴露出的真实缺口：registry 的 Normalize 曾只收录 camelCase
// 形式（reporttaskoutcome / waitagent），漏掉带下划线的 legacy 形式
// （report_task_outcome / wait_agent），于是"同一个工具两个规范名"的旧病会以
// "registry 解析不出来、broker 解析得出来"的形式复发。
func TestRegistryResolvesEveryLegacySpelling(t *testing.T) {
	var problems []string
	for legacy, canonical := range legacyToolAliases {
		if got := toolregistry.Canonical(legacy); got != canonical {
			problems = append(problems, fmt.Sprintf("legacy spelling %q must resolve to %q in toolregistry, got %q", legacy, canonical, got))
		}
	}
	if len(problems) > 0 {
		t.Fatalf("toolregistry legacy resolution disagrees with the broker naming contract in %d place(s):\n  - %s",
			len(problems), strings.Join(problems, "\n  - "))
	}
}

// TestBrokerDispatchedNamesAreLabelledOrchestrationInRegistry 钉死"派发面与分组面不得各说各话"：
// 凡 broker 会接管派发的名字（`IsBrokerTool == true`，**含别名**），其在 toolregistry 里的
// 所属条目必须归 `orchestration` 组。
//
// 为什么要这条：`/tools` 的 `surface_source` 由 `toolsurface` 走 `broker.IsBrokerTool` 判定
// （`toolsurface.go:66`），而 `/catalog` 的分组来自 `toolregistry.Group`。两侧若对同一工具给出
// 不同族标签，前端就会看到"同一个工具在两个端点属于不同组"——正是本战役要消灭的"两个真源"。
//
// 方向与语料：遍历注册表（toolregistry 是叶子包，不能反向 import broker），判定用 broker 自己的
// 函数，故**无需手抄名单**——新增 broker 工具、或把 broker 认得的别名挂到别的组上，都会立刻变红。
//
// 反向（registry orchestration ⊆ broker 工具）**故意不成立**，因此这里不钉：`subagent_spawn_batch`
// 由 `internal/agent/loop.go:8342` 自建定义、`:3408` 自行派发，不是 broker 工具，但按族归类仍属
// 编排面。可成立的只有本断言这一个方向。
func TestBrokerDispatchedNamesAreLabelledOrchestrationInRegistry(t *testing.T) {
	broker := &Broker{}
	checked := 0
	var problems []string
	for _, entry := range toolregistry.Entries() {
		names := make([]string, 0, 1+len(entry.Aliases))
		names = append(names, entry.Name)
		names = append(names, entry.Aliases...)
		for _, name := range names {
			if !broker.IsBrokerTool(name) {
				continue
			}
			checked++
			if entry.Group != toolregistry.GroupOrchestration {
				problems = append(problems, fmt.Sprintf(
					"%q（条目 %s）由 broker 接管派发，但注册表把它归在 %q 组；应归 %q",
					name, entry.Name, entry.Group, toolregistry.GroupOrchestration))
			}
		}
	}
	// 语料下限：防止断言"因为什么都没扫到"而假绿（今天实测 74 个名字）。
	if checked < 40 {
		t.Fatalf("only %d broker-dispatched registry names were checked; the assertion would silently pass", checked)
	}
	if len(problems) > 0 {
		t.Fatalf("broker dispatch surface and registry groups disagree in %d place(s):\n  - %s",
			len(problems), strings.Join(problems, "\n  - "))
	}
}
