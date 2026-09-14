/**
 * 轨迹时间线配色表（P2-9）：第一个时间轴看**消息类型**，状态泳道看**运行状态**。
 *
 * 颜色与明细行图标（`trajectory-view.tsx` 的 `KIND_TEXT_COLORS`）语义一致：
 * 同一个 kind 同一个色系，图上色块与列表行能靠颜色对上号。
 */
import type {
  TrajectoryItemKind,
  TrajectoryItemStatus,
} from "@/lib/trajectory/types";

/** 消息类型配色（第一个时间轴）：用户绿 / 助手蓝 / 工具金 / 推理青 / 编排紫 / 系统灰。 */
export const KIND_BAR_CLASS: Record<TrajectoryItemKind, string> = {
  user: "bg-[#4ade80]",
  assistant: "bg-[#6ea8fe]",
  reasoning: "bg-[#2dd4bf]",
  tool: "bg-[#f0b429]",
  planning: "bg-[#a78bfa]",
  orchestration: "bg-[#a78bfa]",
  route: "bg-[#a78bfa]",
  observation: "bg-[#a78bfa]",
  subagent: "bg-[#a78bfa]",
  result: "bg-[#a78bfa]",
  system: "bg-[#8a8f98]",
};

/** 状态配色（状态泳道 / 工具泳道）：失败红 / 运行蓝 / 取消琥珀 / 待定灰 / 完成绿。 */
export const STATUS_BAR_CLASS: Record<TrajectoryItemStatus, string> = {
  failed: "bg-[#f87171]",
  running: "bg-[#6ea8fe]",
  canceled: "bg-[#fbbf24]",
  pending: "bg-[#8a8f98]",
  completed: "bg-[#4ade80]",
};
