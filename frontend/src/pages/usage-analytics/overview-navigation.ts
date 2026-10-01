// Overview 分类使用独立的 view 参数，避免与会话详情的 tab 参数互相干扰。
export const overviewViews = ["overview", "models", "sessions", "quota", "routing", "lsp", "artifacts"] as const;
export type OverviewView = (typeof overviewViews)[number];

export function resolveOverviewView(value: string | null): OverviewView {
  return overviewViews.includes(value as OverviewView) ? value as OverviewView : "overview";
}

export function selectOverviewView(current: URLSearchParams, view: OverviewView): URLSearchParams {
  const next = new URLSearchParams(current);
  if (view === "overview") next.delete("view");
  else next.set("view", view);
  return next;
}

export function resetOverviewFilters(current: URLSearchParams): URLSearchParams {
  // 重置筛选不应把正在查看的分类切回总览。
  return selectOverviewView(new URLSearchParams(), resolveOverviewView(current.get("view")));
}
