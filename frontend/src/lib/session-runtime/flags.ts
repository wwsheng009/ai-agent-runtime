/**
 * Batch 2 回滚开关：多会话运行时注册表。
 *
 * 当前为 **true**：2026-09-17 以真实 provider（opencode.ai / deepseek-v4.1-flash /
 * effort=max）跑通 `e2e/zz-multi-session-live.manual.ts` H1–H5 后开启（证据见
 * `frontend/.artifacts/live-multi-session/*.json` 与计划文档 §0.1）。
 *
 * 置 false = 改造前行为（只有前台会话订阅运行时流，侧栏沿用单会话活动投影），
 * 即 §6.3 的回滚动作；`true` 时 workspace 才挂载注册表与后台订阅策略
 * （§4.2 / §4.6）。与既有 `COMPOSER_ATTACHMENT_UPLOAD_READY` 同形态：常量开关，
 * 回滚只需改回 false；不引入环境变量，避免"部署形态"进入行为分支。
 */
export const MULTI_SESSION_REGISTRY_ENABLED = true;

/** 注册表连接预算（§4.6）：live 总数 = 前台 1 + 后台 2（可配置）。 */
export const DEFAULT_FOREGROUND_LIVE_BUDGET = 1;
export const DEFAULT_BACKGROUND_LIVE_BUDGET = 2;
