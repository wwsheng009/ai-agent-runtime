// P2-1A：运行时 Skills REST 客户端（技能市场 / 热重载面板）。
//
// 端点（backend/internal/api/skills/handler.go:654-671、852-863）：
//   GET  /api/runtime/skills?source_layer=&source_dir=   → {skills, count}
//   GET  /api/runtime/skills/{name}                      → Skill（404 skill not found）
//   GET  /api/runtime/skills/search?q=&limit=&category=&mode=&source_layer=&source_dir=
//   GET  /api/runtime/skills/stats?source_layer=&source_dir=
//   GET  /api/runtime/skills/hot-reload/stats            → {stats}（未配置时 503）
//   POST /api/runtime/skills/hot-reload/start|stop|reload（写操作）
//
// 归一化纪律：
//   * 核心结构缺失（`skills` 非数组、`stats` 非数组/对象、技能缺 `name`）→ 抛错，
//     不把结构异常当作「空市场」；
//   * 单条技能缺 `name` 只丢该条（其余照常呈现），不做整页失败；
//   * `count` / `total_skills` 保留后端上报值，前端不按数组长度改写；
//   * `mutation_policy` / `embedding` 缺失 → 置 null（UI 显示未知），不猜默认值；
//   * 写操作是受策略约束的端点：403（未授权 / 只读策略 / 策略禁用目录）与 503
//     （热重载未配置）按真实状态码向上抛，UI 分类展示，绝不假装成功。
//
// P0-2 拆分：本文件转为 barrel（原单文件 skills.ts 超 500 非空行门禁）。
// 实现按职责落在 constants/types/normalize/queries/mutations/errors；
// 对外导入路径 `@/api/runtime/skills` 与全部导出名保持不变。

export * from "./constants";
export * from "./types";
export * from "./normalize";
export * from "./queries";
export * from "./mutations";
export * from "./errors";
