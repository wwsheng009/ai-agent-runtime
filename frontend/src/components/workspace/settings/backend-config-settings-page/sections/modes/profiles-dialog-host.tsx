// Profiles 生命周期对话框宿主（纯装配）：按 dialog.kind 选择对应对话框并接线回调。
//
// 纪律：这里只做「状态 → 对话框」的映射，不持有任何状态；保存中标记沿用
// `pending` 前缀约定（`create` / `rename:<ref>` / `delete:<ref>`），
// 宿主与 profiles.tsx 的 pending 计算必须保持一致，否则按钮会假死。

import type {
  RuntimeProfileCreateRequest,
  RuntimeProfileImportReport,
  RuntimeProfileListEntry,
} from "@/types/runtime";

import { ProfileCreateDialog } from "./profile-create-dialog";
import { ProfileImportDialog } from "./profile-import-dialog";
import {
  ProfileDeleteDialog,
  ProfileDuplicateDialog,
  ProfileMoveDialog,
  ProfileReferencesDialog,
  ProfileRenameDialog,
} from "./profile-lifecycle-dialogs";
import type { ProfileReferenceItem } from "./profile-list-utils";

export type ProfilesDialogState =
  | { kind: "create" }
  | { kind: "import"; isPreviewing: boolean; preview: RuntimeProfileImportReport | null }
  | { kind: "rename"; entry: RuntimeProfileListEntry }
  | { kind: "duplicate"; entry: RuntimeProfileListEntry }
  | { kind: "move"; entry: RuntimeProfileListEntry }
  | { kind: "delete"; entry: RuntimeProfileListEntry; referenceCount: number }
  | {
      kind: "references";
      entry: RuntimeProfileListEntry;
      isLoading: boolean;
      references: ProfileReferenceItem[];
    };

export type ProfilesDialogHostProps = {
  dialog: ProfilesDialogState | null;
  /** 新建对话框的「以既有 profile 为模板」下拉数据源。 */
  entries: RuntimeProfileListEntry[];
  pending: string | null;
  onClose: () => void;
  onCreate: (request: RuntimeProfileCreateRequest) => void;
  onDelete: (entry: RuntimeProfileListEntry, force: boolean) => void;
  onDuplicate: (
    entry: RuntimeProfileListEntry,
    name: string,
    layer: RuntimeProfileListEntry["layer"],
  ) => void;
  /** 导入预演（dry_run=true，不落盘）。 */
  onImportPreview: (bundle: File, name: string, layer: string) => void;
  /** 真实导入（落位后绝不自动激活，D28）。 */
  onImportSubmit: (bundle: File, name: string, layer: string) => void;
  onMove: (entry: RuntimeProfileListEntry, layer: RuntimeProfileListEntry["layer"]) => void;
  onRename: (entry: RuntimeProfileListEntry, name: string) => void;
};

export function ProfilesDialogHost({
  dialog,
  entries,
  onClose,
  onCreate,
  onDelete,
  onDuplicate,
  onImportPreview,
  onImportSubmit,
  onMove,
  onRename,
  pending,
}: ProfilesDialogHostProps) {
  if (!dialog) {
    return null;
  }

  switch (dialog.kind) {
    case "create":
      return (
        <ProfileCreateDialog
          isSaving={pending === "create"}
          profiles={entries}
          onCancel={onClose}
          onSubmit={onCreate}
        />
      );
    case "import":
      return (
        <ProfileImportDialog
          isPreviewing={dialog.isPreviewing}
          isSaving={pending === "import"}
          preview={dialog.preview}
          onCancel={onClose}
          onPreview={onImportPreview}
          onSubmit={onImportSubmit}
        />
      );
    case "rename":
      return (
        <ProfileRenameDialog
          entry={dialog.entry}
          isSaving={pending === `rename:${dialog.entry.ref}`}
          onCancel={onClose}
          onSubmit={(name) => {
            onRename(dialog.entry, name);
          }}
        />
      );
    case "duplicate":
      return (
        <ProfileDuplicateDialog
          entry={dialog.entry}
          isSaving={pending === `duplicate:${dialog.entry.ref}`}
          onCancel={onClose}
          onSubmit={(name, layer) => {
            onDuplicate(dialog.entry, name, layer);
          }}
        />
      );
    case "move":
      return (
        <ProfileMoveDialog
          entry={dialog.entry}
          isSaving={pending === `move:${dialog.entry.ref}`}
          onCancel={onClose}
          onSubmit={(layer) => {
            onMove(dialog.entry, layer);
          }}
        />
      );
    case "delete":
      return (
        <ProfileDeleteDialog
          entry={dialog.entry}
          isSaving={pending === `delete:${dialog.entry.ref}`}
          referenceCount={dialog.referenceCount}
          onCancel={onClose}
          onSubmit={(force) => {
            onDelete(dialog.entry, force);
          }}
        />
      );
    case "references":
      return (
        <ProfileReferencesDialog
          entry={dialog.entry}
          isLoading={dialog.isLoading}
          references={dialog.references}
          onCancel={onClose}
        />
      );
    default:
      return null;
  }
}
