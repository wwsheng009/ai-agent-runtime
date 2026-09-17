// 供应商筛选：纯函数，供 composer 模型面板的「供应商」一级列表复用。
// 独立成模块，避免继续膨胀 lib/composer/model-panel-model.ts（P0-2 行数约束）。

/** 归一化查询串：去首尾空白后小写，便于不区分大小写匹配。 */
export function normalizeProviderQuery(query: string): string {
  return query.trim().toLowerCase();
}

/** 按查询串筛选供应商候选；空查询原样返回，不改变候选顺序。 */
export function filterProviderOptions(
  providerOptions: readonly string[],
  query: string,
): readonly string[] {
  const keyword = normalizeProviderQuery(query);
  if (!keyword) {
    return providerOptions;
  }
  return providerOptions.filter((option) => option.toLowerCase().includes(keyword));
}

/** 是否展示「无匹配供应商」空态：有查询串且筛选后没有候选。 */
export function isProviderFilterEmpty(
  providerOptions: readonly string[],
  query: string,
): boolean {
  if (!normalizeProviderQuery(query)) {
    return false;
  }
  return filterProviderOptions(providerOptions, query).length === 0;
}
