// P1-4 子片 2 / S5：Composer 附件草稿轨——纯模型与校验，React/DOM 无关。
//
// 边界（方案 §2 P1-4 / §6.3 P2-1C）：
// - 后端 `POST /api/runtime/uploads` 已就绪：新增附件即上传，成功后条目携带服务端 path；
// - 状态机 `uploading → uploaded | error`；`remotePath` 只在 `uploaded` 有值，
//   **绝不伪造「已上传」**（失败保留本地预览与可行动原因）；
// - 对象 URL 的创建/回收由 owner hook 负责，这里只承载数据与纯决策。

export const COMPOSER_ATTACHMENT_MAX_COUNT = 8;
export const COMPOSER_ATTACHMENT_MAX_BYTES = 20 * 1024 * 1024;

/** 上传接口已就绪（§6.3 P2-1C 后端交付）：附件上传成功后可随消息发送。 */
export const COMPOSER_ATTACHMENT_UPLOAD_READY = true;

export type ComposerAttachmentKind = "image" | "file";

/** 上传状态：在途 / 已上传 / 失败（失败可重试或移除）。 */
export type ComposerAttachmentStatus = "uploading" | "uploaded" | "error";

/**
 * 失败分类（与 api/runtime/uploads 的「不可用 / 校验失败 / 真实失败」三段对齐）：
 * `rejected` 表示请求成功但该文件被后端跳过（超体积上限 / 不是可识别图片）。
 */
export type ComposerAttachmentErrorKind =
  | "unavailable"
  | "validation"
  | "rejected"
  | "failed";

export type ComposerAttachment = {
  id: string;
  kind: ComposerAttachmentKind;
  name: string;
  size: number;
  mimeType: string;
  /** 本地对象 URL（仅图片有值，由 owner hook 创建与回收）。 */
  previewUrl: string | null;
  status: ComposerAttachmentStatus;
  /** 服务端落盘路径（`Uploaded` 时来自上传响应；其余状态恒为 null）。 */
  remotePath: string | null;
  /** 后端说明（跳过原因 / 压缩说明）；无说明为空串。 */
  remoteNote: string;
  /** 失败原因（可行动文案）；仅 `error` 有值。 */
  error: string | null;
  /** 失败分类；仅 `error` 有值（渲染层据此选本地化前缀）。 */
  errorKind: ComposerAttachmentErrorKind | null;
  /** 原始文件（就绪前仅驻留内存，不做持久化）。 */
  file: File;
};

/** 与文件对象解耦的最小字段集：校验/去重只依赖这三项。 */
export type ComposerAttachmentCandidate = {
  name: string;
  size: number;
  type: string;
};

export type ComposerAttachmentRejectionReason =
  | "duplicate"
  | "too-large"
  | "over-limit";

export type ComposerAttachmentRejection = {
  reason: ComposerAttachmentRejectionReason;
  name: string;
};

export type ComposerAttachmentPlan = {
  accepted: ComposerAttachmentCandidate[];
  rejections: ComposerAttachmentRejection[];
};

/** 身份来源：既接受原始候选（`type`），也接受已落轨附件（`mimeType`）。 */
export type ComposerAttachmentIdentitySource = {
  name: string;
  size: number;
  type?: string;
  mimeType?: string;
};

export type ComposerAttachmentLimits = {
  maxCount?: number;
  maxBytes?: number;
};

export function classifyComposerAttachment(type: string): ComposerAttachmentKind {
  return type.startsWith("image/") ? "image" : "file";
}

export function composerAttachmentIdentity(
  candidate: ComposerAttachmentIdentitySource,
): string {
  const mime = candidate.type ?? candidate.mimeType ?? "";
  return `${candidate.name}\u0000${candidate.size}\u0000${mime}`;
}

/** 拖放事件是否携带文件（`dataTransfer.types` 含 `Files`）。 */
export function hasComposerFilePayload(
  types: readonly string[] | undefined | null,
): boolean {
  return Array.from(types ?? []).includes("Files");
}

/**
 * 有序追加计划：保持入参顺序，重复/超大/超限的文件按原因拒收。
 * 判定顺序 duplicate → too-large → over-limit；一旦达到上限，其余一律拒收。
 */
export function planComposerAttachmentAdd(
  current: readonly ComposerAttachment[],
  incoming: readonly ComposerAttachmentCandidate[],
  options: ComposerAttachmentLimits = {},
): ComposerAttachmentPlan {
  const maxCount = options.maxCount ?? COMPOSER_ATTACHMENT_MAX_COUNT;
  const maxBytes = options.maxBytes ?? COMPOSER_ATTACHMENT_MAX_BYTES;
  const seen = new Set(current.map(composerAttachmentIdentity));
  const accepted: ComposerAttachmentCandidate[] = [];
  const rejections: ComposerAttachmentRejection[] = [];

  for (const candidate of incoming) {
    const identity = composerAttachmentIdentity(candidate);
    if (seen.has(identity)) {
      rejections.push({ reason: "duplicate", name: candidate.name });
      continue;
    }
    if (candidate.size > maxBytes) {
      rejections.push({ reason: "too-large", name: candidate.name });
      continue;
    }
    if (current.length + accepted.length >= maxCount) {
      rejections.push({ reason: "over-limit", name: candidate.name });
      continue;
    }
    seen.add(identity);
    accepted.push(candidate);
  }

  return { accepted, rejections };
}

export function createComposerAttachment(
  file: File,
  options: { id: string; previewUrl?: string | null },
): ComposerAttachment {
  return {
    id: options.id,
    kind: classifyComposerAttachment(file.type),
    name: file.name,
    size: file.size,
    mimeType: file.type,
    previewUrl: options.previewUrl ?? null,
    // 新建即进入上传在途；上传结果只能来自服务端响应（见 hook）。
    status: "uploading",
    remotePath: null,
    remoteNote: "",
    error: null,
    errorKind: null,
    file,
  };
}

/** 已上传成功的服务端路径（按轨内顺序；空串与未成功项一律剔除）。 */
export function composerAttachmentUploadedPaths(
  attachments: readonly ComposerAttachment[],
): string[] {
  return attachments
    .filter((item) => item.status === "uploaded" && item.remotePath !== null)
    .map((item) => item.remotePath as string)
    .filter((path) => path !== "");
}

/** 仍有在途 / 失败附件：此时禁止发送（不静默丢弃，也不发送未上传项）。 */
export function hasUnsettledComposerAttachments(
  attachments: readonly ComposerAttachment[],
): boolean {
  return attachments.some((item) => item.status !== "uploaded");
}

export function countUnsettledComposerAttachments(
  attachments: readonly ComposerAttachment[],
): number {
  return attachments.filter((item) => item.status !== "uploaded").length;
}

/** 单次上传响应落成条目状态；只认后端给出的 path，绝不从本地推断「已上传」。 */
export type ComposerAttachmentUploadOutcome =
  | { status: "uploaded"; remotePath: string; remoteNote: string }
  | { status: "error"; errorKind: "rejected"; error: string };

/**
 * 把上传响应归一成单个附件的终态。
 *
 * 多份上传时后端按 `file` 顺序回条目；这里先按文件名匹配，再退化为
 * 「只有一条时取该条」。被跳过（`skipped`）或没有路径一律判失败，
 * 原因用后端 `note` 原文（为空则由渲染层给本地化兜底）。
 */
export function resolveComposerAttachmentUploadOutcome(
  result: { attachments: readonly { name: string; path: string; note: string; skipped: boolean }[] },
  fileName: string,
): ComposerAttachmentUploadOutcome {
  const entries = result.attachments;
  const entry =
    entries.find((item) => item.name === fileName) ??
    (entries.length === 1 ? entries[0] : null);
  if (!entry) {
    return { status: "error", errorKind: "rejected", error: "" };
  }
  if (!entry.skipped && entry.path) {
    return { status: "uploaded", remotePath: entry.path, remoteNote: entry.note };
  }
  return { status: "error", errorKind: "rejected", error: entry.note };
}

/** 展示用尺寸（与语言无关，不做本地化单位换行）。 */
export function formatComposerAttachmentSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  if (bytes < 1024) {
    return `${Math.round(bytes)} B`;
  }
  const kb = bytes / 1024;
  if (kb < 1024) {
    return `${Math.round(kb)} KB`;
  }
  return `${(kb / 1024).toFixed(1)} MB`;
}
