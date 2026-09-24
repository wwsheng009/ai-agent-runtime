// P0-2 行数门禁：ProfilesModeSection 的「生命周期动作」（rename / duplicate / move /
// delete）拆到独立 hook，组件文件保持 ≤ 500 非空行。
//
// 纪律（与拆分前逐字一致，行为零变化）：
//   * ref 是唯一句柄：rename / move 之后必须用响应里的新 ref 重索引列表；
//   * 删除遇到「仍有引用」时把引用数回填到对话框，由用户显式勾选强制删除，
//     而不是把引用信息丢在一条红错里。

import { useCallback, type Dispatch, type SetStateAction } from "react";

import {
  deleteRuntimeProfile,
  duplicateRuntimeProfile,
  listRuntimeProfileReferences,
  moveRuntimeProfile,
  readProfileDeleteBlockingReferences,
  renameRuntimeProfile,
} from "@/api/runtime/profiles";
import type { RuntimeProfileListEntry, RuntimeProfileView } from "@/types/runtime";

import { formatProfileError, type Translate } from "../../profiles/profile-i18n";
import {
  entryFromMutation,
  entryFromMutationResult,
  profileBlockingCount,
  reindexProfileEntries,
} from "./profile-list-utils";
import type { ProfilesDialogState } from "./profiles-dialog-host";

export type UseProfileLifecycleActionsOptions = {
  /** runtimeConfig 命名空间的 t（文案仍留在调用方组件，本 hook 不新增键）。 */
  t: Translate;
  setEntries: Dispatch<SetStateAction<RuntimeProfileListEntry[]>>;
  setView: Dispatch<SetStateAction<RuntimeProfileView | null>>;
  setDialog: Dispatch<SetStateAction<ProfilesDialogState | null>>;
  setPending: Dispatch<SetStateAction<string | null>>;
  setError: Dispatch<SetStateAction<string | null>>;
  setStatusMessage: Dispatch<SetStateAction<string | null>>;
};

export function useProfileLifecycleActions({
  t,
  setEntries,
  setView,
  setDialog,
  setPending,
  setError,
  setStatusMessage,
}: UseProfileLifecycleActionsOptions) {
  const runRename = useCallback(
    async (entry: RuntimeProfileListEntry, name: string) => {
      setPending(`rename:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const result = await renameRuntimeProfile(entry.ref, { name });
        // rename 响应是 new_ref/root/layer（没有 ref/name/path），与列表条目形状不同：
        // 必须显式挑选字段，否则列表会继续持有已失效的旧 ref 句柄。
        const next = entryFromMutation(entry, {
          ref: result.newRef || entry.ref,
          name,
          layer: result.layer,
          path: result.root,
        });
        setEntries((current) => reindexProfileEntries(current, entry.ref, next));
        setView((current) =>
          current && current.ref === entry.ref
            ? { ...current, ref: next.ref, name: next.name }
            : current,
        );
        setDialog(null);
        setStatusMessage(t("profiles.lifecycle.succeeded"));
      } catch (renameError) {
        setError(formatProfileError(renameError, t("profiles.messages.renameFailed")));
      } finally {
        setPending(null);
      }
    },
    [setDialog, setEntries, setError, setPending, setStatusMessage, setView, t],
  );

  const runDuplicate = useCallback(
    async (entry: RuntimeProfileListEntry, name: string, layer: string) => {
      setPending(`duplicate:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const next = await duplicateRuntimeProfile(entry.ref, { name, layer });
        setEntries((current) => [...current, entryFromMutationResult(next)]);
        setDialog(null);
        setStatusMessage(t("profiles.lifecycle.succeeded"));
      } catch (duplicateError) {
        setError(formatProfileError(duplicateError, t("profiles.messages.duplicateFailed")));
      } finally {
        setPending(null);
      }
    },
    [setDialog, setEntries, setError, setPending, setStatusMessage, t],
  );

  const runMove = useCallback(
    async (entry: RuntimeProfileListEntry, layer: string) => {
      setPending(`move:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const result = await moveRuntimeProfile(entry.ref, { layer });
        // move 只换根目录与层级（ref 不变）：层级取本次请求的目标层，路径取响应的 to，
        // 否则列表徽标会停在旧层级、path 指向已迁移走的目录。
        const next = entryFromMutation(entry, {
          ref: result.ref || entry.ref,
          layer,
          path: result.to || entry.path,
        });
        setEntries((current) => reindexProfileEntries(current, entry.ref, next));
        setView((current) =>
          current && current.ref === entry.ref
            ? { ...current, ref: next.ref, layer: next.layer }
            : current,
        );
        setDialog(null);
        setStatusMessage(t("profiles.lifecycle.succeeded"));
      } catch (moveError) {
        setError(formatProfileError(moveError, t("profiles.messages.moveFailed")));
      } finally {
        setPending(null);
      }
    },
    [setDialog, setEntries, setError, setPending, setStatusMessage, setView, t],
  );

  const runDelete = useCallback(
    async (entry: RuntimeProfileListEntry, force: boolean) => {
      setPending(`delete:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const result = await deleteRuntimeProfile(entry.ref, { force });
        if (!result.deleted) {
          // 200 但未删除（防御性分支）：按引用阻断处理，不假装删除成功。
          const references = await listRuntimeProfileReferences(entry.ref).catch(() => null);
          setDialog({ kind: "delete", entry, referenceCount: profileBlockingCount(references) });
          setError(result.error || t("profiles.messages.deleteFailed"));
          return;
        }
        setEntries((current) => current.filter((item) => item.ref !== entry.ref));
        setView((current) => (current && current.ref === entry.ref ? null : current));
        setDialog(null);
        setStatusMessage(t("profiles.lifecycle.succeeded"));
      } catch (deleteError) {
        // 被引用阻断时后端返回 409，错误体里带 references：回填阻断数，
        // 让用户显式勾选强制删除，而不是把引用信息丢在一条红错里。
        const blocked = readProfileDeleteBlockingReferences(deleteError);
        if (blocked) {
          setDialog({ kind: "delete", entry, referenceCount: profileBlockingCount(blocked) });
          setError(formatProfileError(deleteError, t("profiles.messages.deleteBlocked")));
          return;
        }
        setError(formatProfileError(deleteError, t("profiles.messages.deleteFailed")));
      } finally {
        setPending(null);
      }
    },
    [setDialog, setEntries, setError, setPending, setStatusMessage, setView, t],
  );

  return { runRename, runDuplicate, runMove, runDelete };
}
