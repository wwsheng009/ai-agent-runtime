/**
 * P1-5 golden 测试向量：TS reducer 与 TUI encoder 语义对拍
 *
 * 对齐 `backend/cmd/aicli/ui/render/encoding/encoder_test.go` 的语义用例：
 * - 事件向量为「语义层」共享序列（TUI 消费 bus 事件，TS 消费 /api/agent/chat
 *   SSE 帧；kind 映射见下方对照表）；
 * - 期望快照为各自渲染模型下的摘要（kind/status/head 序列），已知模型差异
 *   在 `KNOWN_MODEL_DIFFERENCES` 中记录并评审（对齐 todo P1-5 验收标准）。
 *
 * TUI bus 事件 ↔ TS SSE kind 对照：
 *   session_compact_completed（占位） → 无对应（TS 不建模 system 占位）
 *   llm_request_started                  → meta
 *   assistant_delta{sequence}            → chunk{type:text}
 *   assistant.reasoning{nested}          → reasoning
 *   assistant_message（权威 final）      → result + done
 *   tool_started / tool_finished         → tool_start / tool_end（tool_call 为中间态）
 */
import type { TrajectoryEventKind } from "./types";

export interface GoldenEvent {
  kind: TrajectoryEventKind;
  seq: number;
  payload: Record<string, unknown>;
}

export interface GoldenVector {
  /** 向量名（对齐 TUI 用例名）。 */
  name: string;
  /** TUI 参照用例（backend/cmd/aicli/ui/render/encoding/encoder_test.go）。 */
  tuiRef: string;
  events: GoldenEvent[];
  expected: {
    /** Item Kind 序列（TUI: system/assistant/tool_call/tool_output/reasoning；TS: text/reasoning/tool/structured）。 */
    itemKinds: string[];
    /** Item Status 序列。 */
    statuses: string[];
    /** 渲染语义 head 序列（text→content、reasoning→content、tool→name、structured→事件名）。 */
    heads: Array<string | null>;
    lastEventSeq: number;
  };
}

/**
 * 已知模型差异（评审记录，todo P1-5 验收）：
 * 1. 工具：TUI 双 item（tool_call + tool_output，CauseID 关联）；TS 折叠单 item
 *    （tool_start→tool_call→tool_end 状态机，status: started→running→finished）。
 * 2. system 占位：TUI 用 legacy 事件创建 system cell；SSE 无对应事件，TS 不建模
 *    （chat 投影仅在 error/note 时输出 callout）。
 * 3. reasoning 呈现：TUI 加分隔线（"─ reasoning ─"）与 markdown 启发式；TS 投影
 *    保留纯内容，呈现由渲染层（Phase 2 轨迹视图）负责。
 * 4. 权威 final：TUI 依赖 assistant_message 提交 assistant；TS 依赖 result/done
 *    收尾（SSE 语义，done 前保持 running）。
 */
export const KNOWN_MODEL_DIFFERENCES: string[] = [
  "tool: TUI double-item (tool_call+tool_output) vs TS folded single item",
  "system placeholder: TUI legacy event cell vs TS none (callout on error/note)",
  "reasoning presentation: TUI dividers/markdown heuristic vs TS plain content",
  "authoritative final: TUI assistant_message vs TS result/done",
];

/** TestEncodeBasicSequence 语义向量：LLM 流 → 文本块 → 工具链。 */
export const GOLDEN_BASIC_SEQUENCE: GoldenVector = {
  name: "basic-sequence",
  tuiRef: "TestEncodeBasicSequence",
  events: [
    { kind: "meta", seq: 1, payload: { session_id: "s-1", source: "llm_stream" } },
    { kind: "chunk", seq: 2, payload: { type: "text", content: "你" } },
    { kind: "chunk", seq: 3, payload: { type: "text", content: "好" } },
    { kind: "result", seq: 4, payload: { success: true, output: "你好" } },
    { kind: "done", seq: 5, payload: { status: "completed" } },
    {
      kind: "tool_start",
      seq: 6,
      payload: { type: "tool_call", tool_call: { id: "call-1", name: "read_file" } },
    },
    {
      kind: "tool_end",
      seq: 7,
      payload: {
        type: "tool_call",
        tool_call: { id: "call-1", name: "read_file" },
        tool: { output_summary: "file content" },
      },
    },
  ],
  expected: {
    // TUI: [system, assistant, tool_call, tool_output]（见差异 #1/#2）；
    // TS 额外保留 result 结构化块（轨迹视图展示，chat 投影不渲染）。
    itemKinds: ["text", "structured", "tool"],
    statuses: ["completed", "completed", "completed"],
    heads: ["你好", "structured", "read_file"],
    lastEventSeq: 7,
  },
};

/** TestEncodeOutOfOrder 语义向量：乱序 delta 按 seq 有序拼接。 */
export const GOLDEN_OUT_OF_ORDER: GoldenVector = {
  name: "out-of-order",
  tuiRef: "TestEncodeOutOfOrder",
  events: [
    // delta(5) 先到 → 缓冲；1..4 补齐 → "ABCDE"
    { kind: "chunk", seq: 5, payload: { type: "text", content: "E" } },
    { kind: "chunk", seq: 1, payload: { type: "text", content: "A" } },
    { kind: "chunk", seq: 2, payload: { type: "text", content: "B" } },
    { kind: "chunk", seq: 3, payload: { type: "text", content: "C" } },
    { kind: "chunk", seq: 4, payload: { type: "text", content: "D" } },
  ],
  expected: {
    itemKinds: ["text"],
    statuses: ["running"],
    heads: ["ABCDE"],
    lastEventSeq: 5,
  },
};

/** TestEncodeIdempotent 语义向量：重复 final / 空 delta 不产生变更。 */
export const GOLDEN_IDEMPOTENT: GoldenVector = {
  name: "idempotent",
  tuiRef: "TestEncodeIdempotent",
  events: [
    { kind: "chunk", seq: 1, payload: { type: "text", content: "hi" } },
    { kind: "result", seq: 2, payload: { success: true, output: "hi" } },
    { kind: "done", seq: 3, payload: { status: "completed" } },
    // 重复 final（幂等：不产生变更）
    { kind: "done", seq: 4, payload: { status: "completed" } },
    // 空 delta（不追加内容）
    { kind: "chunk", seq: 5, payload: { type: "text", content: "" } },
  ],
  expected: {
    itemKinds: ["text", "structured"],
    statuses: ["completed", "completed"],
    heads: ["hi", "structured"],
    lastEventSeq: 5,
  },
};

/** TestEncodeReasoningIndependentOfAssistant 语义向量：reasoning 独立 item。 */
export const GOLDEN_REASONING_INDEPENDENT: GoldenVector = {
  name: "reasoning-independent",
  tuiRef: "TestEncodeReasoningIndependentOfAssistant",
  events: [
    { kind: "reasoning", seq: 1, payload: { content: "thinking..." } },
    { kind: "chunk", seq: 2, payload: { type: "text", content: "Hello" } },
    { kind: "result", seq: 3, payload: { success: true, output: "Hello" } },
    { kind: "done", seq: 4, payload: { status: "completed" } },
  ],
  expected: {
    // reasoning 绝不覆盖 assistant（独立 Item，TUI 同语义）；result 结构化块保留。
    itemKinds: ["reasoning", "text", "structured"],
    statuses: ["completed", "completed", "completed"],
    heads: ["thinking...", "Hello", "structured"],
    lastEventSeq: 4,
  },
};

export const GOLDEN_VECTORS: GoldenVector[] = [
  GOLDEN_BASIC_SEQUENCE,
  GOLDEN_OUT_OF_ORDER,
  GOLDEN_IDEMPOTENT,
  GOLDEN_REASONING_INDEPENDENT,
];
