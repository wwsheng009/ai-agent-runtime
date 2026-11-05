/**
 * 失败模式展示分类的共享判定。
 *
 * `render_fence_drop_idle` 是 CLI 渲染围栏在 run 尚未开始时丢弃镜像事件的
 * 诊断信号，不是 provider/tool 执行失败；面板和图表必须用同一口径把它标成
 * “非故障诊断”，避免用户把它当成真实错误。判定以分类/错误码为准，
 * 不依赖来源字段缺失时也能保持显示语义一致。
 */
export function isNonFaultDiagnosticCategory(category?: string): boolean {
  return (category ?? "").trim().toLowerCase() === "render_fence_drop_idle";
}

export function isNonFaultDiagnosticPattern(pattern: {
  failure_category?: string;
  error_code?: string;
}): boolean {
  if (isNonFaultDiagnosticCategory(pattern.failure_category)) {
    return true;
  }
  // failure_category 缺失时仍按稳定错误码识别，避免后端契约演进期间退化。
  return (pattern.error_code ?? "").trim().toUpperCase() === "RENDER_FENCE_DROPPED_IDLE";
}
