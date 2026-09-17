// 面板候选筛选：纯函数，供 composer 模型面板的「供应商 / 模型」二级列表复用。
// 独立成模块，避免继续膨胀 lib/composer/model-panel-model.ts（P0-2 行数约束）。

/** 归一化查询串：去首尾空白后小写，便于不区分大小写匹配。 */
export function normalizeOptionQuery(query: string): string {
  return query.trim().toLowerCase();
}

/** 按查询串筛选候选；空查询原样返回，不改变候选顺序。 */
export function filterOptionValues(
  options: readonly string[],
  query: string,
): readonly string[] {
  const keyword = normalizeOptionQuery(query);
  if (!keyword) {
    return options;
  }
  return options.filter((option) => option.toLowerCase().includes(keyword));
}

/** 是否展示「无匹配」空态：有查询串且筛选后没有候选。 */
export function isOptionFilterEmpty(
  options: readonly string[],
  query: string,
): boolean {
  if (!normalizeOptionQuery(query)) {
    return false;
  }
  return filterOptionValues(options, query).length === 0;
}
