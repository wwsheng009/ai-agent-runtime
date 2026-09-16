// 错误摘要：把 unknown 收敛成「给人看的一行说明」。
//
// 纪律：只透传真实的 Error message（运行时/后端结论），不改写、不美化、不猜测原因；
// 拿不到 message 时退化为 String(error)，绝不返回「未知错误」这类无信息文案。
//
// 独立成模块的原因：多个面板（git 变更列表、git 面板外壳）共用同一口径，
// 且组件文件导出非组件会被 `react-refresh/only-export-components` 拦下。

/** 错误摘要：只透传运行时/后端的 message，不改写语义。 */
export function describeError(error: unknown): string {
  if (!error) {
    return "";
  }
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}
