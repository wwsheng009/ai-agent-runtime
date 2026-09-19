// 通用页签条的测试 id 工厂：与组件分文件，避免 react-refresh 规则
// （组件文件只导出组件；非组件导出放共享模块，见 `workspace-shell-shared.ts` 同款约定）。

/** 页签按钮的 data-testid。 */
export function tabTestId(testIdBase: string, id: string): string {
  return `${testIdBase}-${id}`;
}

/** 页签关闭按钮的 data-testid。 */
export function closeTabTestId(testIdBase: string, id: string): string {
  return `${testIdBase}-close-${id}`;
}
