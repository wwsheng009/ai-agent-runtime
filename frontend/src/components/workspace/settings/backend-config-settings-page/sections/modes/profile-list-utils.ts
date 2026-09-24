// Profiles 列表纯函数与导出下载辅助：过滤 / 条目合并 / ref 变更后的重索引。
//
// 抽成独立模块的理由：这些规则（ref 是唯一句柄、rename/move 会换 ref、保存后
// 要用 resolved view 覆盖列表条目）是列表一致性的核心，必须在无 DOM 的情况下可测。
//
// 契约纪律（Batch 8 对账后）：
//   * 列表条目字段严格等于后端 runtimeProfileEntry：
//     ref / name / description / layer / path / valid / error / is_default /
//     default_agent / writable；没有 status、active 这类「想象字段」；
//   * rename / move 的响应形状不同（newRef vs ref），因此由调用方挑好再传进来，
//     这里只做「缺字段保留原值」的合并；
//   * 引用面（references）是结构化对象，UI 需要的是可渲染列表，转换放这里。

import type {
  RuntimeProfileCreateResponse,
  RuntimeProfileExportBundle,
  RuntimeProfileListEntry,
  RuntimeProfileReferencesResponse,
  RuntimeProfileView,
} from "@/types/runtime";

/**
 * 触发浏览器保存导出的 zip 包（POST 响应体 → Blob → 临时对象 URL）。
 *
 * jsdom 不实现 `URL.createObjectURL`：能力缺失时显式抛错，由调用方走错误提示，
 * 而不是静默「成功但没下载」。revoke 紧跟 click（与 lib/trajectory/export.ts 同节奏）。
 */
export function downloadProfileBundle(bundle: RuntimeProfileExportBundle) {
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") {
    throw new Error("browser does not support blob downloads");
  }
  const url = URL.createObjectURL(bundle.blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = bundle.filename;
    anchor.rel = "noopener";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** 按名称 / ref / 描述 / 层级做大小写不敏感过滤；空关键字原样返回。 */
export function filterProfileEntries(
  entries: RuntimeProfileListEntry[],
  query: string,
): RuntimeProfileListEntry[] {
  const keyword = query.trim().toLowerCase();
  if (!keyword) {
    return entries;
  }
  return entries.filter((entry) =>
    [entry.name, entry.ref, entry.description, entry.layer].some((field) =>
      field.toLowerCase().includes(keyword),
    ),
  );
}

/** resolved view 里的第一条错误文案（view 没有 error 字段，错误在 issues 里）。 */
export function profileViewError(view: RuntimeProfileView): string {
  const first = view.issues.find((issue) => issue.severity === "error");
  return first ? first.message || first.path : "";
}

/** 用 resolved view 覆盖列表条目：保存后列表要立刻反映新名称/描述/校验状态。 */
export function mergeProfileView(
  entry: RuntimeProfileListEntry,
  view: RuntimeProfileView,
): RuntimeProfileListEntry {
  return {
    ...entry,
    ref: view.ref,
    name: view.name,
    description: view.description,
    layer: view.layer,
    path: view.path,
    valid: view.valid,
    error: profileViewError(view),
    defaultAgent: view.defaultAgent,
  };
}

/** rename / move 响应里可回填到列表条目的字段（缺失即保留原值）。 */
export type ProfileMutationPatch = {
  ref?: string;
  name?: string;
  layer?: string;
  path?: string;
};

/** rename / move 的响应 → 列表条目：ref 换了句柄必须跟着换。 */
export function entryFromMutation(
  entry: RuntimeProfileListEntry,
  next: ProfileMutationPatch,
): RuntimeProfileListEntry {
  return {
    ...entry,
    ref: next.ref || entry.ref,
    name: next.name || entry.name,
    layer: next.layer || entry.layer,
    path: next.path || entry.path,
  };
}

/** 新建 / 复制后的占位条目：estimate 等字段要等 getRuntimeProfile 才有。 */
export function entryFromMutationResult(
  next: RuntimeProfileCreateResponse,
): RuntimeProfileListEntry {
  return {
    ref: next.ref,
    name: next.name || next.ref,
    description: "",
    layer: next.layer,
    path: next.root,
    valid: true,
    error: "",
    isDefault: false,
    defaultAgent: "",
    writable: true,
    // 新建/复制响应不携带 D29 信任上下文：新写出的 profile 尚未经过列表投影，
    // 置为"未扣留"，下一次列表刷新会以服务端结论为准。
    promptSuppressed: false,
    promptSuppressionReason: "",
  };
}

/**
 * rename / move 会换 ref：替换旧条目并去重。
 * 新 ref 已存在时以「已存在的那条」为准，避免列表出现重复句柄。
 */
export function reindexProfileEntries(
  entries: RuntimeProfileListEntry[],
  previousRef: string,
  next: RuntimeProfileListEntry,
): RuntimeProfileListEntry[] {
  const existing = entries.find((entry) => entry.ref === next.ref && entry.ref !== previousRef);
  if (existing) {
    // 目标 ref 已被占用（后端语义上等同「同名同层已存在」）：只丢掉旧句柄那一行，
    // 保留已存在条目的字段——用 next 覆盖会把新 profile 的名称/描述错挂到旧句柄上。
    return entries.filter((entry) => entry.ref !== previousRef);
  }
  return entries.map((entry) => (entry.ref === previousRef ? next : entry));
}

/** 引用面条目：blocking（阻断删除）/ warning（提醒）/ file（受影响文件）。 */
export type ProfileReferenceItem = {
  kind: "blocking" | "warning" | "file";
  text: string;
};

/**
 * 引用面响应 → 可渲染列表。
 * 后端 references 是 `{blocking, warnings, files, configItems, ...}` 结构，
 * 这里按「是否需要人工处理」排序：先阻断，再警告，最后是文件清单。
 */
export function profileReferenceItems(
  response: RuntimeProfileReferencesResponse | null,
): ProfileReferenceItem[] {
  if (!response) {
    return [];
  }
  const items: ProfileReferenceItem[] = [];
  for (const text of response.warnings) {
    items.push({ kind: "warning", text });
  }
  for (const text of response.blocking) {
    items.push({ kind: "blocking", text });
  }
  for (const file of response.files) {
    items.push({ kind: "file", text: file });
  }
  return items;
}

/** 引用面里的阻断条数：删除被拒时用来提示「需要强制删除」。 */
export function profileBlockingCount(
  response: RuntimeProfileReferencesResponse | null,
): number {
  return response ? response.blocking.length : 0;
}
