// 事件契约前端门禁（方案 docs/plan/sse-live-event-channel-optimization-plan.md
// §4 Batch 2）。后端一侧由 internal/events/contract_test.go 断言「注册表 ↔
// runtimeobserve 已知类型目录」一致；本文件补齐前端一侧：注册表里每一个落盘
// 类型，必须被前端**恰好一条**消费路径接住，否则新增类型漂移的症状只是
// 「前端没反应」，与「本来就没有事件」不可区分（P0-1）。
//
// 判定对象是三条真实消费路径：
// - 助手增量（打字机）：lib/trajectory/recovery 的 ASSISTANT_RUNTIME_EVENT_TYPES
//   → lib/thread-state/deltas 的 getRuntimeDeltaKind；
// - 通用生命周期行：lib/trajectory/recovery 的 RUNTIME_EVENT_TYPES（派生自生成物）；
// - 工具帧桥：lib/thread-state/deltas 的 getRuntimeBridgeKind。
// provenance 承载类型（wire-only）显式声明为「不建行」，同样计入覆盖。
import { describe, expect, it } from "vitest";

import {
  CHAT_SSE_EVENT_PREFIX,
  RUNTIME_EVENT_CHAT_BRIDGE_TYPES,
  RUNTIME_EVENT_LIVE_ONLY_TYPES,
  RUNTIME_EVENT_PERSISTED_TYPES,
  RUNTIME_EVENT_PROVENANCE_TYPES,
  RUNTIME_EVENT_TAIL_ONLY_TYPES,
} from "@/types/runtime/event-contract";
import {
  ASSISTANT_RUNTIME_EVENT_TYPES,
  RUNTIME_EVENT_TYPES,
} from "@/lib/trajectory/recovery";
import {
  getRuntimeBridgeKind,
  getRuntimeDeltaKind,
} from "@/lib/thread-state/deltas";

const sorted = (values: readonly string[]) => [...values].sort();

// 双通道白名单：生产者自行落盘、同时靠尾巴帧补发的类型（后端 contract.go 里
// ChannelSessionStore | ChannelTailOnly，写侧由 ProducerPersistedEvent 去重）。
// 除此之外，落盘类型与 live-only / tail-only 仍互斥。
//
// 口径 = 生成物里同时含 session_store 与 tail_only 的类型（当前 7 个）。注册表
// 扩容新增双通道类型时本清单需同步，否则下面的互斥断言先失败（它正是这么用的）。
const DUAL_CHANNEL_TAIL_TYPES = new Set<string>([
  "subagent.batch.canceled",
  "subagent.batch.failed",
  "subagent.batch.orphaned",
  "subagent.batch.timed_out",
  "subagent.completed",
  "subagent.route.resolved",
  "subagent.suspension.unavailable",
]);

describe("runtime 事件契约（前端消费路径覆盖）", () => {
  it("每个落盘类型都有且只有一条前端消费路径（不静默丢弃）", () => {
    const uncovered: string[] = [];
    const multiCovered: string[] = [];
    for (const type of RUNTIME_EVENT_PERSISTED_TYPES) {
      const paths = [
        ASSISTANT_RUNTIME_EVENT_TYPES.has(type) ? "assistant-delta" : "",
        RUNTIME_EVENT_TYPES.has(type) ? "runtime-row" : "",
        getRuntimeBridgeKind(type) ? "tool-bridge" : "",
        RUNTIME_EVENT_PROVENANCE_TYPES.includes(type) ? "provenance-skip" : "",
      ].filter(Boolean);
      if (paths.length === 0) {
        uncovered.push(type);
      }
      if (paths.length > 1) {
        // 多路径命中 = 同一事实渲染两次（P0-2 的构造性双写），必须显式排查。
        multiCovered.push(`${type} (${paths.join("+")})`);
      }
    }
    expect(
      uncovered,
      `落盘类型未被任何前端路径接住：${uncovered.join(", ")}`,
    ).toEqual([]);
    expect(
      multiCovered,
      `落盘类型命中多条前端路径：${multiCovered.join(", ")}`,
    ).toEqual([]);
  });

  it("落盘的 assistant* 增量全部有种类分类（漏归类 = 打字机静默丢弃）", () => {
    const assistantPersisted = RUNTIME_EVENT_PERSISTED_TYPES.filter((type) =>
      type.startsWith("assistant"),
    );
    expect(assistantPersisted.length).toBeGreaterThan(0);
    for (const type of assistantPersisted) {
      expect(
        getRuntimeDeltaKind(type),
        `${type} 未被 getRuntimeDeltaKind 归类`,
      ).not.toBeNull();
    }
    // 未登记进契约的历史别名在存量会话里仍会出现，必须继续归类。
    expect(getRuntimeDeltaKind("assistant.delta")).toBe("text");
    expect(getRuntimeDeltaKind("assistant.reasoning_delta")).toBe("reasoning");
    // 终稿标记不是增量：误归类会把整条消息当增量追加。
    expect(getRuntimeDeltaKind("assistant_message")).toBeNull();
  });

  it("chat_bridge 契约类型与 store 侧改名都有桥接分类", () => {
    for (const type of RUNTIME_EVENT_CHAT_BRIDGE_TYPES) {
      expect(getRuntimeBridgeKind(type), `${type} 无桥接分类`).not.toBeNull();
    }
    // tool.requested / tool.completed 落盘时被改名为 tool_started / tool_finished
    // （api/skills 的 mapRuntimeEventToSession），两条拼写都会出现在流上。
    for (const alias of ["tool_started", "tool_finished"]) {
      expect(getRuntimeBridgeKind(alias), `${alias} 无桥接分类`).not.toBeNull();
    }
    for (const frame of ["tool_start", "tool_call", "tool_end", "observation", "chunk"]) {
      expect(
        getRuntimeBridgeKind(`${CHAT_SSE_EVENT_PREFIX}${frame}`),
        `${frame} 帧无桥接分类`,
      ).not.toBeNull();
    }
    // 孪生副本刻意不归类（否则推理行会被两路写成「跑 / 停」交替）——回归锁。
    expect(getRuntimeBridgeKind(`${CHAT_SSE_EVENT_PREFIX}reasoning`)).toBeNull();
  });

  it("provenance 承载类型不建轨迹行（Batch 1 的 wire-only 语义）", () => {
    for (const type of RUNTIME_EVENT_PROVENANCE_TYPES) {
      expect(RUNTIME_EVENT_TYPES.has(type), `${type} 不应建通用行`).toBe(false);
      expect(
        ASSISTANT_RUNTIME_EVENT_TYPES.has(type),
        `${type} 不应走增量路径`,
      ).toBe(false);
      expect(getRuntimeBridgeKind(type), `${type} 不应建工具行`).toBeNull();
    }
  });

  it("通道分层互斥：落盘类型不会同时声明 live-only / tail-only（双通道白名单除外）", () => {
    const persisted = new Set(RUNTIME_EVENT_PERSISTED_TYPES);
    for (const type of RUNTIME_EVENT_LIVE_ONLY_TYPES) {
      expect(persisted.has(type), `${type} 同时声明落盘与 live-only`).toBe(false);
    }
    for (const type of RUNTIME_EVENT_TAIL_ONLY_TYPES) {
      if (DUAL_CHANNEL_TAIL_TYPES.has(type)) {
        expect(persisted.has(type), `${type} 双通道白名单必须同时落盘`).toBe(true);
        continue;
      }
      expect(persisted.has(type), `${type} 同时声明落盘与 tail-only`).toBe(false);
    }
  });

  it("前端派生名单回归锁（Batch 2 派生口径 + 注册表扩容同步）", () => {
    // 落盘生命周期行 = PERSISTED − 助手增量 − 工具帧桥 − provenance（派生口径见
    // lib/trajectory/recovery.ts）。注册表扩容后需同步本清单：2026-09-22 `071f128e`
    // 补齐 46 个已登记类型（main_agent.route_* / llm.* / subagent.batch.* 等）后，
    // 本名单由 13 → 28 条——锁的语义是「每次扩容都被显式复核」，不是「永远等于
    // Batch 2 当天的字面量」。
    expect(sorted([...RUNTIME_EVENT_TYPES])).toEqual([
      "agent.reclaimed",
      "approval_requested",
      "approval_resolved",
      "checkpoint_created",
      "context_reconciled",
      "llm.prompt_cache.breaker_tripped",
      "llm.provider.health_opened",
      "main_agent.route_applied",
      "main_agent.route_cleared",
      "main_agent.route_cost_guard_tripped",
      "main_agent.route_disabled_for_turn",
      "main_agent.route_prediction_invalid",
      "main_agent.route_prediction_unresolvable",
      "session.routing_changed",
      "session_compact_completed",
      "session_compact_failed",
      "session_compact_skipped",
      "session_compact_started",
      "session_end",
      "session_interrupted",
      "session_start",
      "subagent.batch.canceled",
      "subagent.batch.failed",
      "subagent.batch.orphaned",
      "subagent.batch.timed_out",
      "subagent.completed",
      "subagent.route.resolved",
      "subagent.suspension.unavailable",
    ]);
    expect(sorted([...ASSISTANT_RUNTIME_EVENT_TYPES])).toEqual([
      "assistant.image_progress",
      "assistant.reasoning",
      "assistant.reasoning_delta",
      "assistant_delta",
      "assistant_reasoning",
    ]);
  });
});
