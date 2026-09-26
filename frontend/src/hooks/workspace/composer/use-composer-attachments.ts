import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  describeRuntimeUploadFailure,
  isRuntimeUploadValidationError,
  isRuntimeUploadsUnavailable,
  uploadRuntimeAttachment,
} from "@/api/runtime/uploads";
import {
  composerAttachmentIdentity,
  composerAttachmentUploadedPaths,
  countUnsettledComposerAttachments,
  createComposerAttachment,
  hasComposerFilePayload,
  planComposerAttachmentAdd,
  resolveComposerAttachmentUploadOutcome,
  type ComposerAttachment,
  type ComposerAttachmentErrorKind,
} from "@/lib/composer-attachments";

import type { RuntimeUploadResult } from "@/types/runtime";

export type UseComposerAttachmentsOptions = {
  /** 会话键（与草稿同口径：sessionId 优先、线程 id 兜底）；切换会话附件互不串扰。 */
  threadKey: string | null;
  maxCount?: number;
  maxBytes?: number;
  /** 测试注入：对象 URL 工厂（缺省用浏览器 `URL.createObjectURL`）。 */
  createObjectUrl?: (file: File) => string | null;
  /** 测试注入：对象 URL 回收（缺省用浏览器 `URL.revokeObjectURL`）。 */
  revokeObjectUrl?: (url: string) => void;
  /** 测试注入：附件 id 工厂。 */
  createId?: () => string;
  /** 测试注入：上传实现（缺省为 `POST /api/runtime/uploads`）。 */
  uploadFile?: (file: File, options: { signal: AbortSignal }) => Promise<RuntimeUploadResult>;
};

export type ComposerAttachmentsController = {
  attachments: ComposerAttachment[];
  /** 全视口拖放邀请的可见性（拖拽文件进出窗口时切换）。 */
  isDragOver: boolean;
  /** 最近一次被拒收的文件数（0 表示无）；由组件渲染 aria-live 提示后确认清零。 */
  rejectedCount: number;
  /** 在途上传数。 */
  uploadingCount: number;
  /** 已上传成功数。 */
  uploadedCount: number;
  /** 尚未落定（在途或失败）的附件数：>0 时禁止发送，避免静默丢弃。 */
  unsettledCount: number;
  /** 已上传成功的服务端路径（发送时作为 `submit_prompt.images` 载荷）。 */
  uploadedPaths: string[];
  addFiles: (files: FileList | readonly File[]) => void;
  removeAttachment: (id: string) => void;
  clearAttachments: () => void;
  /** 失败重试（保留本地预览与条目身份，重新走一次上传）。 */
  retryAttachment: (id: string) => void;
  acknowledgeRejections: () => void;
};

const UNBOUND_THREAD_KEY = "\u0000unbound";
const EMPTY_ATTACHMENTS: ComposerAttachment[] = [];

let composerAttachmentSeq = 0;

function defaultCreateId(): string {
  composerAttachmentSeq += 1;
  return `composer-attachment-${composerAttachmentSeq}`;
}

function defaultCreateObjectUrl(file: File): string | null {
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") {
    return null;
  }
  try {
    return URL.createObjectURL(file);
  } catch {
    return null;
  }
}

function defaultRevokeObjectUrl(url: string): void {
  if (typeof URL === "undefined" || typeof URL.revokeObjectURL !== "function") {
    return;
  }
  try {
    URL.revokeObjectURL(url);
  } catch {
    // 已释放或不可用：忽略，绝不断输入。
  }
}

/** 上传异常 → 条目失败态：分类与 api 层三段判据一一对应，原因保留后端原文。 */
export function classifyComposerAttachmentUploadError(error: unknown): {
  errorKind: ComposerAttachmentErrorKind;
  error: string;
} {
  const failure = describeRuntimeUploadFailure(error);
  if (isRuntimeUploadsUnavailable(error)) {
    return { errorKind: "unavailable", error: failure.reason };
  }
  if (isRuntimeUploadValidationError(error)) {
    return { errorKind: "validation", error: failure.reason };
  }
  return { errorKind: "failed", error: failure.reason };
}

type UploadTicket = {
  key: string;
  attempt: number;
  controller: AbortController;
};

/**
 * P1-4 子片 2 / S5：附件草稿轨的 owner（数据 + 上传状态）。
 *
 * - 三段式入口（file input / 粘贴 / 全视口拖放）共用 `addFiles`，有序追加；
 * - **新增即上传**：条目先入轨（`uploading` + 本地预览），成功才带服务端 path
 *   （`uploaded`），失败落在 `error` 并保留预览与可行动原因，可重试或移除；
 * - 重试与竞态：每个附件持有「尝试序号 + AbortController」，旧响应的结果一律丢弃；
 *   移除 / 清空 / 卸载会中止在途上传（主动取消不落错误态）；
 * - 图片对象 URL 在移除/清空/卸载时回收；文件本体不落盘，刷新即清空；
 * - 组件只渲染，不持有附件数据。
 */
export function useComposerAttachments({
  threadKey,
  maxCount,
  maxBytes,
  createObjectUrl,
  revokeObjectUrl,
  createId,
  uploadFile,
}: UseComposerAttachmentsOptions): ComposerAttachmentsController {
  const storageKey = threadKey ?? UNBOUND_THREAD_KEY;
  const limits = useMemo(() => ({ maxCount, maxBytes }), [maxCount, maxBytes]);
  const objectUrlApi = useMemo(
    () => ({
      create: createObjectUrl ?? defaultCreateObjectUrl,
      revoke: revokeObjectUrl ?? defaultRevokeObjectUrl,
    }),
    [createObjectUrl, revokeObjectUrl],
  );
  const idFactory = useMemo(() => createId ?? defaultCreateId, [createId]);
  const uploaderRef = useRef(uploadFile ?? uploadRuntimeAttachment);
  useEffect(() => {
    uploaderRef.current = uploadFile ?? uploadRuntimeAttachment;
  }, [uploadFile]);

  const [byThread, setByThread] = useState<
    Record<string, ComposerAttachment[]>
  >({});
  const [isDragOver, setIsDragOver] = useState(false);
  const [rejectedCount, setRejectedCount] = useState(0);

  // 事件处理器读最新映射（渲染期不读）；effect 兜底同步，避免半路回滚。
  const byThreadRef = useRef(byThread);
  const revokeRef = useRef(objectUrlApi.revoke);
  useEffect(() => {
    byThreadRef.current = byThread;
  }, [byThread]);
  useEffect(() => {
    revokeRef.current = objectUrlApi.revoke;
  }, [objectUrlApi]);

  const attachments = byThread[storageKey] ?? EMPTY_ATTACHMENTS;

  // 在途上传票据（附件 id → 尝试）。卸载时按票据中止全部在途请求。
  const uploadsRef = useRef(new Map<string, UploadTicket>());
  const attemptSeqRef = useRef(0);

  const commit = useCallback((next: Record<string, ComposerAttachment[]>) => {
    byThreadRef.current = next;
    setByThread(next);
  }, []);

  /** 就地更新某个附件；附件已不在轨内时返回 false（不复活已移除的条目）。 */
  const patchAttachment = useCallback(
    (key: string, id: string, updater: (item: ComposerAttachment) => ComposerAttachment) => {
      const current = byThreadRef.current[key] ?? EMPTY_ATTACHMENTS;
      let changed = false;
      const next = current.map((item) => {
        if (item.id !== id) {
          return item;
        }
        const updated = updater(item);
        changed = changed || updated !== item;
        return updated;
      });
      if (!changed) {
        return false;
      }
      commit({ ...byThreadRef.current, [key]: next });
      return true;
    },
    [commit],
  );

  const isCurrentAttempt = useCallback((id: string, attempt: number) => {
    return uploadsRef.current.get(id)?.attempt === attempt;
  }, []);

  const startUpload = useCallback(
    (key: string, item: ComposerAttachment) => {
      attemptSeqRef.current += 1;
      const attempt = attemptSeqRef.current;
      const controller = new AbortController();
      uploadsRef.current.get(item.id)?.controller.abort();
      uploadsRef.current.set(item.id, { key, attempt, controller });

      void (async () => {
        try {
          const result = await uploaderRef.current(item.file, {
            signal: controller.signal,
          });
          if (!isCurrentAttempt(item.id, attempt)) {
            return;
          }
          const outcome = resolveComposerAttachmentUploadOutcome(result, item.name);
          patchAttachment(key, item.id, (current) =>
            outcome.status === "uploaded"
              ? {
                  ...current,
                  status: "uploaded",
                  remotePath: outcome.remotePath,
                  remoteNote: outcome.remoteNote,
                  error: null,
                  errorKind: null,
                }
              : {
                  ...current,
                  status: "error",
                  remotePath: null,
                  remoteNote: "",
                  error: outcome.error,
                  errorKind: outcome.errorKind,
                },
          );
        } catch (error) {
          // 主动中止（移除 / 重试 / 卸载）不是失败，不落错误态。
          if (controller.signal.aborted || !isCurrentAttempt(item.id, attempt)) {
            return;
          }
          const failure = classifyComposerAttachmentUploadError(error);
          patchAttachment(key, item.id, (current) => ({
            ...current,
            status: "error",
            remotePath: null,
            remoteNote: "",
            error: failure.error,
            errorKind: failure.errorKind,
          }));
        } finally {
          if (isCurrentAttempt(item.id, attempt)) {
            uploadsRef.current.delete(item.id);
          }
        }
      })();
    },
    [isCurrentAttempt, patchAttachment],
  );

  const addFiles = useCallback(
    (files: FileList | readonly File[]) => {
      const incoming = Array.from(files);
      if (incoming.length === 0) {
        return;
      }
      const current = byThreadRef.current[storageKey] ?? EMPTY_ATTACHMENTS;
      const plan = planComposerAttachmentAdd(
        current,
        incoming.map((file) => ({
          name: file.name,
          size: file.size,
          type: file.type,
        })),
        limits,
      );
      if (plan.rejections.length > 0) {
        setRejectedCount((count) => count + plan.rejections.length);
      }
      if (plan.accepted.length === 0) {
        return;
      }
      // 拒收项已从 accepted 中剔除，按身份回查原始文件（不能按下标对齐）。
      const fileByIdentity = new Map(
        incoming.map((file) => [
          composerAttachmentIdentity({
            name: file.name,
            size: file.size,
            type: file.type,
          }),
          file,
        ]),
      );
      const accepted = plan.accepted
        .map((candidate) => {
          const file = fileByIdentity.get(composerAttachmentIdentity(candidate));
          if (!file) {
            return null;
          }
          return createComposerAttachment(file, {
            id: idFactory(),
            previewUrl: candidate.type.startsWith("image/")
              ? objectUrlApi.create(file)
              : null,
          });
        })
        .filter((item): item is ComposerAttachment => item !== null);
      if (accepted.length === 0) {
        return;
      }
      commit({
        ...byThreadRef.current,
        [storageKey]: [...current, ...accepted],
      });
      // 入轨后才发起上传：条目先以 uploading 可见，结果只由服务端响应改写。
      for (const item of accepted) {
        startUpload(storageKey, item);
      }
    },
    [commit, idFactory, limits, objectUrlApi, startUpload, storageKey],
  );

  const removeAttachment = useCallback(
    (id: string) => {
      const ticket = uploadsRef.current.get(id);
      if (ticket) {
        uploadsRef.current.delete(id);
        ticket.controller.abort();
      }
      const current = byThreadRef.current[storageKey] ?? EMPTY_ATTACHMENTS;
      const target = current.find((item) => item.id === id);
      if (!target) {
        return;
      }
      if (target.previewUrl) {
        objectUrlApi.revoke(target.previewUrl);
      }
      commit({
        ...byThreadRef.current,
        [storageKey]: current.filter((item) => item.id !== id),
      });
    },
    [commit, objectUrlApi, storageKey],
  );

  const retryAttachment = useCallback(
    (id: string) => {
      const current = byThreadRef.current[storageKey] ?? EMPTY_ATTACHMENTS;
      const target = current.find((item) => item.id === id);
      if (!target || target.status === "uploading") {
        return;
      }
      const reset = patchAttachment(storageKey, id, (item) => ({
        ...item,
        status: "uploading",
        remotePath: null,
        remoteNote: "",
        error: null,
        errorKind: null,
      }));
      if (reset) {
        startUpload(storageKey, { ...target, status: "uploading" });
      }
    },
    [patchAttachment, startUpload, storageKey],
  );

  const clearAttachments = useCallback(() => {
    const current = byThreadRef.current[storageKey] ?? EMPTY_ATTACHMENTS;
    for (const item of current) {
      const ticket = uploadsRef.current.get(item.id);
      if (ticket) {
        uploadsRef.current.delete(item.id);
        ticket.controller.abort();
      }
      if (item.previewUrl) {
        objectUrlApi.revoke(item.previewUrl);
      }
    }
    const next = { ...byThreadRef.current };
    delete next[storageKey];
    commit(next);
  }, [commit, objectUrlApi, storageKey]);

  const acknowledgeRejections = useCallback(() => {
    setRejectedCount(0);
  }, []);

  // 全视口拖放：dragenter/dragleave 成对计数，避免跨越子元素时邀请闪烁。
  const dragDepthRef = useRef(0);
  useEffect(() => {
    const handleDragEnter = (event: DragEvent) => {
      if (!hasComposerFilePayload(event.dataTransfer?.types)) {
        return;
      }
      event.preventDefault();
      dragDepthRef.current += 1;
      setIsDragOver(true);
    };
    const handleDragOver = (event: DragEvent) => {
      if (!hasComposerFilePayload(event.dataTransfer?.types)) {
        return;
      }
      event.preventDefault();
    };
    const handleDragLeave = (event: DragEvent) => {
      if (!hasComposerFilePayload(event.dataTransfer?.types)) {
        return;
      }
      dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
      if (dragDepthRef.current === 0) {
        setIsDragOver(false);
      }
    };
    const handleDrop = (event: DragEvent) => {
      if (!hasComposerFilePayload(event.dataTransfer?.types)) {
        return;
      }
      event.preventDefault();
      dragDepthRef.current = 0;
      setIsDragOver(false);
      const dropped = event.dataTransfer?.files;
      if (dropped && dropped.length > 0) {
        addFiles(dropped);
      }
    };

    window.addEventListener("dragenter", handleDragEnter);
    window.addEventListener("dragover", handleDragOver);
    window.addEventListener("dragleave", handleDragLeave);
    window.addEventListener("drop", handleDrop);
    return () => {
      window.removeEventListener("dragenter", handleDragEnter);
      window.removeEventListener("dragover", handleDragOver);
      window.removeEventListener("dragleave", handleDragLeave);
      window.removeEventListener("drop", handleDrop);
    };
  }, [addFiles]);

  // 卸载时中止全部在途上传并回收全部预览 URL（含其他会话的草稿轨）。
  useEffect(() => {
    const uploads = uploadsRef.current;
    return () => {
      for (const ticket of uploads.values()) {
        ticket.controller.abort();
      }
      uploads.clear();
      for (const list of Object.values(byThreadRef.current)) {
        for (const item of list) {
          if (item.previewUrl) {
            revokeRef.current(item.previewUrl);
          }
        }
      }
      byThreadRef.current = {};
    };
  }, []);

  const uploadingCount = attachments.filter(
    (item) => item.status === "uploading",
  ).length;
  const uploadedCount = attachments.filter(
    (item) => item.status === "uploaded",
  ).length;

  return {
    attachments,
    isDragOver,
    rejectedCount,
    uploadingCount,
    uploadedCount,
    unsettledCount: countUnsettledComposerAttachments(attachments),
    uploadedPaths: composerAttachmentUploadedPaths(attachments),
    addFiles,
    removeAttachment,
    clearAttachments,
    retryAttachment,
    acknowledgeRejections,
  };
}
