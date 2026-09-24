// P0-2 拆分（原 skills.ts 请求类型区）：查询/变更的请求选项类型集中于此。

export type SkillsRequestOptions = {
  signal?: AbortSignal;
  /** 来源层级过滤（后端 `source_layer`）。 */
  layer?: string;
  /** 来源目录过滤（后端 `source_dir`）。 */
  dir?: string;
};

export type SkillSearchQuery = {
  query: string;
  limit?: number;
  category?: string;
  mode?: RuntimeSkillSearchModeInput;
};

export type RuntimeSkillSearchModeInput = "auto" | "lexical" | "semantic";

export type SkillMutationRequestOptions = {
  signal?: AbortSignal;
  /** 管理令牌；空串即不发送鉴权头（后端按 loopback/role 判定）。 */
  adminToken?: string;
};
