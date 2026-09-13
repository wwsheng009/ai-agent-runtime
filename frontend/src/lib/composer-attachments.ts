// P1-4 子片 2：Composer 附件（本地草稿轨）——纯模型与校验，React/DOM 无关。
//
// 边界（方案 §2 P1-4 / §6.3 P2-1C）：
// - 后端 `POST /runtime/uploads` 未就绪，本子片只做「本地预览 + 待发送」；
// - 附件状态**恒为 `pending`**，接口就绪前不产生远端 URL、不伪造成已上传；
// - 对象 URL 的创建/回收由 owner hook 负责，这里只承载数据与纯决策。

export const COMPOSER_ATTACHMENT_MAX_COUNT = 8;
export const COMPOSER_ATTACHMENT_MAX_BYTES = 20 * 1024 * 1024;

/** 上传接口就绪后翻转为 true（届时附件可随消息发送）。 */
export const COMPOSER_ATTACHMENT_UPLOAD_READY = false;

export type ComposerAttachmentKind = "image" | "file";

/** 上传状态：接口就绪前恒为 `pending`（界面上的「待发送」）。 */
export type ComposerAttachmentStatus = "pending";

export type ComposerAttachment = {
  id: string;
  kind: ComposerAttachmentKind;
  name: string;
  size: number;
  mimeType: string;
  /** 本地对象 URL（仅图片有值，由 owner hook 创建与回收）。 */
  previewUrl: string | null;
  status: ComposerAttachmentStatus;
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
    status: "pending",
    file,
  };
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
