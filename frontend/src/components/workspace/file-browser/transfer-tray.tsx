// 工作区右侧栏「文件浏览器面」→ 传输队列展示层（P2-4）。
//
// 分工（lint 拆分后，勿把逻辑塞回本文件）：
//   * 下载三层选择与能力说明的纯逻辑 → `transfer-model.ts`（常量 / 类型 / selectDownloadLayer / layerNotes）；
//   * 下载执行（探测、分片写入、流式落盘） → `use-download-manager.ts`（useDownloadManager）。
//   * 本文件只做**渲染**：把 useFileTransfer / useDownloadManager 的状态画出来，不自行发请求、不判能力。
//
// 归一化纪律：能力缺失的说明由 model 层给出的 `transfer.layer.*` key 渲染；
//   进度只用服务端确认值（upload offset / Range 写入字节数），不预测、不补零。
import { AlertTriangleIcon, CheckCircle2Icon, DownloadIcon, PauseIcon, PlayIcon, UploadIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { formatByteSize } from "@/lib/file-preview/decode";
import { cn } from "@/lib/utils";

import type { DownloadTask } from "@/components/workspace/file-browser/transfer-model";
import type { RetainedUploadMetadata, UploadTask } from "@/hooks/workspace/use-file-transfer";
import type { FsConflictPolicy } from "@/types/runtime/fs-browser";

export type TransferTrayProps = {
  uploads: readonly UploadTask[];
  downloads: readonly DownloadTask[];
  retained: readonly RetainedUploadMetadata[];
  onPauseUpload: (id: string) => void;
  onResumeUpload: (id: string) => void;
  onCancelUpload: (id: string) => void;
  onResolveConflict: (id: string, policy: Exclude<FsConflictPolicy, "fail">) => void;
  onDismissUpload: (id: string) => void;
  onCancelDownload: (id: string) => void;
  onDismissDownload: (id: string) => void;
  onClose: () => void;
  className?: string;
};

type TrayAction = { icon: typeof XIcon; label: string; onClick: () => void };

/** 传输队列展示层：只渲染 `useFileTransfer` / `useDownloadManager` 的状态，不自行发请求。 */
export function TransferTray(props: TransferTrayProps) {
  const { t } = useTranslation("workspace");
  const {
    className, downloads, onCancelDownload, onCancelUpload, onClose, onDismissDownload, onDismissUpload,
    onPauseUpload, onResolveConflict, onResumeUpload, retained, uploads,
  } = props;
  const progress = (received: number, total: number) =>
    t("panels.fileBrowser.transfer.progress", { received: formatByteSize(received), total: formatByteSize(total) });
  const uploadActions = (task: UploadTask): TrayAction[] => {
    const name = task.name;
    if (task.state === "completed" || task.state === "failed" || task.state === "canceled") {
      return [{ icon: CheckCircle2Icon, label: t("panels.fileBrowser.transfer.dismiss", { name }), onClick: () => onDismissUpload(task.id) }];
    }
    const actions: TrayAction[] = [];
    if (task.state === "uploading" || task.state === "queued") {
      actions.push({ icon: PauseIcon, label: t("panels.fileBrowser.transfer.pause", { name }), onClick: () => onPauseUpload(task.id) });
    }
    if (task.state === "paused") {
      actions.push({ icon: PlayIcon, label: t("panels.fileBrowser.transfer.resume", { name }), onClick: () => onResumeUpload(task.id) });
    }
    actions.push({ icon: XIcon, label: t("panels.fileBrowser.transfer.cancel", { name }), onClick: () => onCancelUpload(task.id) });
    return actions;
  };
  const downloadActions = (task: DownloadTask): TrayAction[] =>
    task.state === "probing" || task.state === "downloading" || task.state === "saving"
      ? [{ icon: XIcon, label: t("panels.fileBrowser.transfer.cancel", { name: task.name }), onClick: () => onCancelDownload(task.id) }]
      : [{ icon: CheckCircle2Icon, label: t("panels.fileBrowser.transfer.dismiss", { name: task.name }), onClick: () => onDismissDownload(task.id) }];

  return (
    <aside
      aria-label={t("panels.fileBrowser.transfer.ariaLabel")}
      className={cn("grid max-h-64 min-h-0 gap-2 overflow-auto rounded-card border border-border/60 bg-surface/60 p-2 text-xs", className)}
      data-testid="file-browser-transfer-tray"
    >
      <div className="flex items-center gap-2">
        <span className="font-medium text-foreground">{t("panels.fileBrowser.transfer.title")}</span>
        <span className="text-muted-foreground">
          {t("panels.fileBrowser.transfer.uploads")} {uploads.length} · {t("panels.fileBrowser.transfer.downloads")} {downloads.length}
        </span>
        <button aria-label={t("panels.fileBrowser.transfer.close")} className="ml-auto rounded p-0.5 text-muted-foreground hover:bg-white/5" onClick={onClose} title={t("panels.fileBrowser.transfer.close")} type="button">
          <XIcon aria-hidden className="size-3.5" />
        </button>
      </div>

      {uploads.length === 0 && downloads.length === 0 ? <p className="text-muted-foreground">{t("panels.fileBrowser.transfer.empty")}</p> : null}

      {uploads.map((task) => (
        <div className="grid gap-1 rounded border border-border/50 px-2 py-1" data-testid={`upload-task-${task.state}`} key={task.id}>
          <p className="flex items-center gap-1.5">
            <UploadIcon aria-hidden className="size-3 shrink-0 text-muted-foreground" />
            <span className="truncate" title={task.dir ? `${task.dir}/${task.name}` : task.name}>{task.name}</span>
            <span className="ml-auto shrink-0 text-muted-foreground">
              {t(`panels.fileBrowser.transfer.state.${task.state}`)} · {progress(task.offset, task.size)}
            </span>
          </p>
          {task.state === "completed" && task.targetPath ? (
            <p className="text-[11px] text-muted-foreground">{t("panels.fileBrowser.transfer.completedHint", { path: task.targetPath })}</p>
          ) : null}
          {task.state === "conflict" ? (
            <div className="grid gap-1 rounded border border-accent-gold/30 bg-accent-gold/10 p-1.5" data-testid="upload-conflict">
              <p className="font-medium text-foreground">{t("panels.fileBrowser.transfer.conflictTitle", { name: task.name })}</p>
              <p className="text-[11px] text-muted-foreground">{t("panels.fileBrowser.transfer.conflictBody")}</p>
              <p className="text-[11px] text-muted-foreground">{t("panels.fileBrowser.transfer.conflictHint")}</p>
              <div className="flex gap-1.5">
                <TrayButton icon={CheckCircle2Icon} label={t("panels.fileBrowser.transfer.conflictOverwrite")} onClick={() => onResolveConflict(task.id, "overwrite")} />
                <TrayButton icon={PauseIcon} label={t("panels.fileBrowser.transfer.conflictRename")} onClick={() => onResolveConflict(task.id, "rename")} />
              </div>
            </div>
          ) : null}
          {task.state === "failed" ? (
            <p className="inline-flex items-center gap-1 text-[11px] text-accent-gold">
              <AlertTriangleIcon aria-hidden className="size-3" />
              {task.error instanceof Error ? task.error.message : t("panels.fileBrowser.transfer.state.failed")}
            </p>
          ) : null}
          <div className="flex flex-wrap gap-1.5">
            {uploadActions(task).map((action) => <TrayButton icon={action.icon} key={action.label} label={action.label} onClick={action.onClick} />)}
          </div>
        </div>
      ))}

      {downloads.map((task) => (
        <div className="grid gap-1 rounded border border-border/50 px-2 py-1" data-testid={`download-task-${task.state}`} key={task.id}>
          <p className="flex items-center gap-1.5">
            <DownloadIcon aria-hidden className="size-3 shrink-0 text-muted-foreground" />
            <span className="truncate" title={task.path}>{task.name}</span>
            <span className="ml-auto shrink-0 text-muted-foreground">
              {t(`panels.fileBrowser.transfer.download.${task.state}`)} · {progress(task.transferred, task.size)}
            </span>
          </p>
          <p className="text-[11px] text-muted-foreground">
            {t("panels.fileBrowser.transfer.layerHint", { layer: t(`panels.fileBrowser.transfer.layer.${task.layer}`) })}
          </p>
          {(task.notes ?? []).map((note) => (
            <p className="text-[11px] text-accent-gold" data-note-key={note.key} key={note.key}>
              {/* 动态键：键集合由 transfer-model.ts 的 layerNotes 收口（字面量联合），沿用仓库既有 `as never` 约定。 */}
              {t(note.key as never, note.params as never) as unknown as string}
            </p>
          ))}
          <div className="flex flex-wrap gap-1.5">
            {downloadActions(task).map((action) => <TrayButton icon={action.icon} key={action.label} label={action.label} onClick={action.onClick} />)}
          </div>
        </div>
      ))}

      {retained.length > 0 ? (
        <p className="text-[11px] text-muted-foreground" data-testid="retained-uploads">
          {t("panels.fileBrowser.transfer.retainedHint", { count: retained.length })}
        </p>
      ) : null}
    </aside>
  );
}

function TrayButton({ icon: Icon, label, onClick }: { icon: typeof XIcon; label: string; onClick: () => void }) {
  return (
    <button aria-label={label} className="inline-flex items-center gap-1 rounded border border-border/60 px-1.5 py-0.5 text-[11px] text-foreground hover:bg-white/5" onClick={onClick} title={label} type="button">
      <Icon aria-hidden className="size-3" />
      {label}
    </button>
  );
}
