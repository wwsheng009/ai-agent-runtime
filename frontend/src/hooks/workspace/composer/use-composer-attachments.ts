import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  composerAttachmentIdentity,
  createComposerAttachment,
  hasComposerFilePayload,
  planComposerAttachmentAdd,
  type ComposerAttachment,
} from "@/lib/composer-attachments";

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
};

export type ComposerAttachmentsController = {
  attachments: ComposerAttachment[];
  /** 全视口拖放邀请的可见性（拖拽文件进出窗口时切换）。 */
  isDragOver: boolean;
  /** 最近一次被拒收的文件数（0 表示无）；由组件渲染 aria-live 提示后确认清零。 */
  rejectedCount: number;
  addFiles: (files: FileList | readonly File[]) => void;
  removeAttachment: (id: string) => void;
  clearAttachments: () => void;
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

/**
 * P1-4 子片 2：附件草稿轨的 owner（数据 + 上传状态）。
 *
 * - 三段式入口（file input / 粘贴 / 全视口拖放）共用 `addFiles`，有序追加；
 * - 上传接口未就绪（§6.3 P2-1C），附件状态恒为 `pending`，只做本地预览；
 * - 图片对象 URL 在移除/清空/卸载时回收；存储与文件都不落盘，刷新即清空；
 * - 组件只渲染，不持有附件数据。
 */
export function useComposerAttachments({
  threadKey,
  maxCount,
  maxBytes,
  createObjectUrl,
  revokeObjectUrl,
  createId,
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

  const commit = useCallback((next: Record<string, ComposerAttachment[]>) => {
    byThreadRef.current = next;
    setByThread(next);
  }, []);

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
      const accepted = plan.accepted.map((candidate) => {
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
      }).filter((item): item is ComposerAttachment => item !== null);
      if (accepted.length === 0) {
        return;
      }
      commit({
        ...byThreadRef.current,
        [storageKey]: [...current, ...accepted],
      });
    },
    [commit, idFactory, limits, objectUrlApi, storageKey],
  );

  const removeAttachment = useCallback(
    (id: string) => {
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

  const clearAttachments = useCallback(() => {
    const current = byThreadRef.current[storageKey] ?? EMPTY_ATTACHMENTS;
    for (const item of current) {
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

  // 卸载时回收全部预览 URL（含其他会话的草稿轨）。
  useEffect(() => {
    return () => {
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

  return {
    attachments,
    isDragOver,
    rejectedCount,
    addFiles,
    removeAttachment,
    clearAttachments,
    acknowledgeRejections,
  };
}
