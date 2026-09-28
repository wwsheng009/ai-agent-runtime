package events

// EventRenderFenceDropped：CLI 渲染围栏在 run 结束时上报本轮被拒的 late action
// 计数（诊断面专用，不参与渲染决策）。
//
// 载荷（int，缺省按 0 处理；归属 SessionID，turn_id 可为空）：
//   - idle：本会话尚未开启任何 run 时的拒绝（epoch 0 哨兵的日常噪声）
//   - closed：上一个 run 已结束后的迟到事件
//   - active_mismatch：有活动 run 但 epoch 不匹配（跨 run 混入）
//   - last_reason：最后一次拒绝原因（可选）
//
// 消费端：usageanalytics collector → usage_render_fence_drops 表 →
// /analytics/errors 的 "fence" 来源（P1-1b）。
const EventRenderFenceDropped = "runtime.render_fence_dropped"
