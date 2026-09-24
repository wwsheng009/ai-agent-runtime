// P0-2 拆分（原 skills.ts 常量区）：端点/上限常量集中于此，供 queries/mutations/normalize 共享。

export const SKILLS_PATH = "/api/runtime/skills";
export const SKILLS_SEARCH_PATH = "/api/runtime/skills/search";
export const SKILLS_STATS_PATH = "/api/runtime/skills/stats";
export const SKILLS_HOT_RELOAD_PATH = "/api/runtime/skills/hot-reload";
export const SKILLS_HOT_RELOAD_STATS_PATH = `${SKILLS_HOT_RELOAD_PATH}/stats`;

/** 写操作鉴权头（handler.go:7069-7079 同时接受 Bearer admin token）。 */
export const SKILLS_ADMIN_TOKEN_HEADER = "X-Skills-Admin-Token";

export const DEFAULT_SKILL_SEARCH_LIMIT = 20;
export const MAX_SKILL_SEARCH_LIMIT = 200;
