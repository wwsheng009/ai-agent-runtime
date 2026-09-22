// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

export function readText(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

// expert 并发上限的三态写法（G6）：-1 = 显式不限，正数 = 上限。
//
// 历史写法 0 与 -1 同义（后端在启动期对 0 输出一次 warning）。这里把 0 与非法
// 输入统一归一成 -1，好处有两个：写入配置的「不限」永远是显式的 -1，不再是含混
// 的 0；同时 `-1` 能真正被输入框表达出来——此前 parseNonNegativeInteger 会把
// 用户输入的 -1 静默改成 0，等于「想限流却写成不限」。
export function parseExpertConcurrency(value: string) {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed >= 1 ? parsed : -1;
}

export function setOptionalText(
  target: Record<string, unknown>,
  key: string,
  value: string,
) {
  const normalized = value.trim();
  if (normalized) {
    target[key] = normalized;
  } else {
    delete target[key];
  }
}
