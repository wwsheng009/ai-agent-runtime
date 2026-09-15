// Phase 2（合并方案 §3.5-D）：平铺模式下的「管理目录」弹层。
// 合并后平铺模式没有目录组头，目录注册表管理必须由本弹层承载，否则功能倒退。
// 行内能力与组头动作对齐：目录内新建会话（自持忙碌态）/ 行内重命名 / 请求移除（由接线层开确认弹窗）；
// 遮罩、Esc 与点击遮罩关闭、按钮基元沿用 workspace-directory-add-dialog / delete-dialog 的既有写法。

import {
  FolderPlusIcon,
  LoaderCircleIcon,
  MessageSquarePlusIcon,
  PencilIcon,
  TrashIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { useEffect, useState, type JSX } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";

import { InlineRenameInput } from "@/components/workspace/workspace-sidebar/session-item";
import { type WorkspaceDirectoryCreateRequest } from "@/components/workspace/workspace-sidebar/types";

/**
 * 方案 §12-A：**仅由会话派生**的目录（会话 metadata.context.workspace_path
 * 有值，但不在工作目录注册表里）。它们此前只能通过「添加目录」手动录入路径纳入管理，
 * 现在在管理弹层里直接列出并给一键「注册」入口。
 */
export type SidebarUnregisteredDirectory = {
  /** 规范化绝对路径（注册表 POST 的入参，与合并分组同一口径）。 */
  path: string;
  /** 展示名：会话派生组的 label（通常是路径末段）。 */
  label: string;
  /** 该目录下当前可见会话数（与注册目录同口径的徽标计数）。 */
  sessionCount: number;
};

export type WorkspaceDirectoryManageDialogProps = {
  open: boolean;
  onClose: () => void;
  /** 注册表里的工作目录（0 会话也要列出）。 */
  directories: readonly RuntimeWorkspaceDirectory[];
  /** 每个目录 id 下的会话数（缺省按 0 显示）。 */
  sessionCounts: Readonly<Record<string, number>>;
  /** 仅由会话派生的目录（未注册）：缺省为空，不渲染该分区。 */
  unregisteredDirectories?: readonly SidebarUnregisteredDirectory[];
  /** 注册一个派生目录（POST 注册表 + 刷新列表，由接线层处理）。 */
  onRegisterDirectory?: (path: string) => Promise<void> | void;
  /** 请求打开「添加目录」弹窗（由接线层处理，本组件不自己开弹窗）。 */
  onRequestAdd: () => void;
  /** 在目录中新建会话：自己维护 in-flight 忙碌态（await 该 Promise）。 */
  onCreateSession: (
    request: WorkspaceDirectoryCreateRequest,
  ) => Promise<void> | void;
  /** 提交目录别名重命名（行内输入，回车提交 / Esc 取消）。 */
  onRenameDirectory: (id: string, name: string) => Promise<void> | void;
  /** 请求移除目录（由接线层打开既有确认弹窗）。 */
  onRequestRemove: (directory: RuntimeWorkspaceDirectory) => void;
  t?: never; // 组件内部自行 useTranslation("workspace")，不要外部传 t
};

/** 组头同款动作按钮样式（含 disabled 视觉）。 */
const ACTION_BUTTON_CLASS =
  "rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground disabled:opacity-50";

/** 别名缺省取路径末段（与侧栏组的兜底口径一致）。 */
function directoryDisplayName(directory: RuntimeWorkspaceDirectory): string {
  const named = directory.name?.trim();
  if (named) {
    return named;
  }
  const normalized = directory.path
    .trim()
    .replace(/\\/g, "/")
    .replace(/\/+$/, "");
  if (normalized === "/" || /^[A-Za-z]:$/.test(normalized)) {
    return normalized;
  }
  return normalized.split("/").filter(Boolean).pop() || directory.path;
}

export function WorkspaceDirectoryManageDialog({
  open,
  onClose,
  directories,
  sessionCounts,
  unregisteredDirectories = [],
  onRegisterDirectory,
  onRequestAdd,
  onCreateSession,
  onRenameDirectory,
  onRequestRemove,
}: WorkspaceDirectoryManageDialogProps): JSX.Element | null {
  const { t } = useTranslation("workspace");
  const [creatingId, setCreatingId] = useState<string | null>(null);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [registeringPath, setRegisteringPath] = useState<string | null>(null);
  /** 注册失败就地提示：弹层会遮住侧栏错误条，所以这里自己显示一条。 */
  const [registerError, setRegisterError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  if (!open || typeof document === "undefined") {
    return null;
  }

  async function handleCreate(directory: RuntimeWorkspaceDirectory) {
    if (creatingId) {
      return;
    }
    setCreatingId(directory.id);
    try {
      await onCreateSession({
        path: directory.path,
        directoryId: directory.id,
        label: directoryDisplayName(directory),
      });
    } catch {
      // 失败提示由接线层统一呈现；本弹层只保证忙碌态回落。
    } finally {
      setCreatingId(null);
    }
  }

  async function handleRenameSubmit(
    directory: RuntimeWorkspaceDirectory,
    nextName: string,
  ) {
    const trimmed = nextName.trim();
    if (!trimmed) {
      setRenamingId(null);
      return;
    }
    try {
      await onRenameDirectory(directory.id, trimmed);
    } catch {
      // 同上：错误展示归属接线层。
    } finally {
      setRenamingId(null);
    }
  }

  async function handleRegister(entry: SidebarUnregisteredDirectory) {
    if (registeringPath || !onRegisterDirectory) {
      return;
    }
    setRegisterError(null);
    setRegisteringPath(entry.path);
    try {
      await onRegisterDirectory(entry.path);
    } catch (registerFailure) {
      // 注册失败（目录已不存在 / 无 home 等）不关闭弹层：就地提示，行仍在。
      setRegisterError(
        registerFailure instanceof Error
          ? registerFailure.message
          : String(registerFailure),
      );
    } finally {
      setRegisteringPath(null);
    }
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[120] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("sidebar.directories.manageTitle")}
        className="w-full max-w-md overflow-hidden rounded-panel border border-border [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]"
      >
        <div className="px-4 py-4">
          <h2 className="text-lg font-semibold tracking-[-0.03em] text-foreground">
            {t("sidebar.directories.manageTitle")}
          </h2>
          <p className="mt-1 text-sm leading-6 text-muted-foreground">
            {t("sidebar.directories.manageHint")}
          </p>

          {directories.length === 0 && unregisteredDirectories.length === 0 ? (
            <p
              data-testid="directory-manage-empty"
              className="mt-4 rounded-field border border-border bg-surface-softer px-3 py-2 text-sm leading-6 text-muted-foreground"
            >
              {t("sidebar.directories.manageEmpty")}
            </p>
          ) : (
            <ul
              data-testid="directory-manage-list"
              className="mt-4 max-h-[52vh] space-y-2 overflow-y-auto"
            >
              {directories.map((directory) => {
                const label = directoryDisplayName(directory);
                const isCreating = creatingId === directory.id;
                const isRenaming = renamingId === directory.id;

                return (
                  <li
                    data-testid="directory-manage-row"
                    data-directory-id={directory.id}
                    key={directory.id}
                    className="rounded-field border border-border bg-surface-softer px-2.5 py-2"
                  >
                    <div className="flex items-center gap-2">
                      <div className="min-w-0 flex-1">
                        <div className="flex min-w-0 items-center gap-1.5">
                          <span
                            className="truncate text-sm font-medium text-foreground"
                            title={label}
                          >
                            {label}
                          </span>
                          {directory.exists === false ? (
                            <span
                              data-testid="directory-manage-warning"
                              className="inline-flex shrink-0 items-center"
                              title={t("sidebar.directories.existsWarning")}
                            >
                              <TriangleAlertIcon
                                size={13}
                                className="text-accent-gold"
                              />
                            </span>
                          ) : null}
                          <span
                            data-testid="directory-manage-count"
                            className="shrink-0"
                          >
                            <Badge>{sessionCounts[directory.id] ?? 0}</Badge>
                          </span>
                        </div>
                        <p
                          className="truncate text-xs leading-5 text-muted-foreground"
                          title={directory.path}
                        >
                          {directory.path}
                        </p>
                      </div>

                      <div className="flex shrink-0 items-center gap-0.5">
                        <button
                          type="button"
                          title={t("sidebar.directories.newChat")}
                          aria-label={t("sidebar.directories.newChat")}
                          disabled={isCreating}
                          onClick={() => {
                            void handleCreate(directory);
                          }}
                          className={ACTION_BUTTON_CLASS}
                        >
                          {isCreating ? (
                            <LoaderCircleIcon size={12} className="animate-spin" />
                          ) : (
                            <MessageSquarePlusIcon size={12} />
                          )}
                        </button>
                        <button
                          type="button"
                          title={t("sidebar.directories.rename")}
                          aria-label={t("sidebar.directories.rename")}
                          onClick={() =>
                            setRenamingId(isRenaming ? null : directory.id)
                          }
                          className={ACTION_BUTTON_CLASS}
                        >
                          <PencilIcon size={12} />
                        </button>
                        <button
                          type="button"
                          title={t("sidebar.directories.deleteTitle")}
                          aria-label={t("sidebar.directories.deleteTitle")}
                          onClick={() => onRequestRemove(directory)}
                          className={ACTION_BUTTON_CLASS}
                        >
                          <TrashIcon size={12} />
                        </button>
                      </div>
                    </div>

                    {isRenaming ? (
                      <div className="mt-1.5">
                        <InlineRenameInput
                          ariaLabel={t("sidebar.directories.rename")}
                          initial={label}
                          onCancel={() => setRenamingId(null)}
                          onSubmit={(nextName) => {
                            void handleRenameSubmit(directory, nextName);
                          }}
                          placeholder={t("sidebar.session.renamePlaceholder")}
                        />
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}

          {/* 方案 §12-A：会话派生的目录此前在管理弹层里完全不可见（只能手动重录路径）。 */}
          {unregisteredDirectories.length > 0 ? (
            <div className="mt-4" data-testid="directory-manage-unregistered">
              <h3 className="text-sm font-medium text-foreground">
                {t("sidebar.directories.manageUnregisteredTitle")}
              </h3>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t("sidebar.directories.manageUnregisteredHint")}
              </p>
              {registerError ? (
                <p
                  role="alert"
                  data-testid="directory-manage-register-error"
                  className="mt-2 rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground"
                >
                  {registerError}
                </p>
              ) : null}
              <ul
                data-testid="directory-manage-unregistered-list"
                className="mt-2 max-h-[34vh] space-y-2 overflow-y-auto"
              >
                {unregisteredDirectories.map((entry) => {
                  const isRegistering = registeringPath === entry.path;

                  return (
                    <li
                      data-testid="directory-manage-unregistered-row"
                      data-directory-path={entry.path}
                      key={entry.path}
                      className="rounded-field border border-dashed border-border bg-surface-softer px-2.5 py-2"
                    >
                      <div className="flex items-center gap-2">
                        <div className="min-w-0 flex-1">
                          <div className="flex min-w-0 items-center gap-1.5">
                            <span
                              className="truncate text-sm font-medium text-muted-foreground"
                              title={entry.label}
                            >
                              {entry.label}
                            </span>
                            <span
                              data-testid="directory-manage-unregistered-count"
                              className="shrink-0"
                            >
                              <Badge>{entry.sessionCount}</Badge>
                            </span>
                          </div>
                          <p
                            className="truncate text-xs leading-5 text-muted-foreground"
                            title={entry.path}
                          >
                            {entry.path}
                          </p>
                        </div>

                        <Button
                          size="sm"
                          disabled={isRegistering || !onRegisterDirectory}
                          title={t("sidebar.directories.register")}
                          aria-label={t("sidebar.directories.register")}
                          onClick={() => {
                            void handleRegister(entry);
                          }}
                        >
                          <span className="inline-flex items-center gap-1">
                            {isRegistering ? (
                              <LoaderCircleIcon
                                size={12}
                                className="animate-spin"
                              />
                            ) : (
                              <FolderPlusIcon size={12} />
                            )}
                            {t("sidebar.directories.register")}
                          </span>
                        </Button>
                      </div>
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : null}

          <div className="mt-4 flex items-center justify-end">
            <Button size="sm" onClick={onRequestAdd}>
              {t("sidebar.directories.add")}
            </Button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
