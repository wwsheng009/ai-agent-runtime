// Profiles 管理模式面板（Batch 8）：列表 + 9 卡片编辑器 + 生命周期操作。
//
// 独立数据流：直接读写 /api/runtime/profiles，不进入 config document 草稿，
// 因此不会触发页面的未保存提示条（unsaved bar）。
//
// 纪律：
//   * ref 是唯一句柄：rename / move 之后必须用响应里的新 ref 重索引列表；
//   * apply 端点在当前后端可能返回 501 not_implemented，此时降级为提示并禁用按钮，
//     不把它当成红错（Batch 12 落地后同一个端点直接可用）；
//   * 删除遇到「仍有引用」时把引用数回填到对话框，由用户显式勾选强制删除。

import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createRuntimeProfile,
  deleteRuntimeProfile,
  duplicateRuntimeProfile,
  exportRuntimeProfile,
  getRuntimeProfile,
  importRuntimeProfile,
  listRuntimeProfileReferences,
  listRuntimeProfiles,
  moveRuntimeProfile,
  readProfileDeleteBlockingReferences,
  renameRuntimeProfile,
  setDefaultRuntimeProfile,
} from "@/api/runtime/profiles";
import { grantHarnessTrust } from "@/api/runtime/harness";
import { useRuntimeClientIdentity } from "@/lib/runtime-client";
import type {
  RuntimeProfileCreateRequest,
  RuntimeProfileListEntry,
  RuntimeProfileView,
} from "@/types/runtime";

import { SettingsEmptyState } from "../../../settings-empty-state";
import { SettingsNoticeCard } from "../../../settings-notice-card";
import { ProfileEditor } from "../../profiles/profile-editor";
import { formatProfileError } from "../../profiles/profile-i18n";
import { ProfileListHeader } from "./profile-list-header";
import { ProfileListRow } from "./profile-list-row";
import { ProfilesTrustNotice } from "./profiles-trust-notice";
import {
  downloadProfileBundle,
  entryFromMutation,
  entryFromMutationResult,
  filterProfileEntries,
  mergeProfileView,
  profileBlockingCount,
  profileReferenceItems,
  reindexProfileEntries,
} from "./profile-list-utils";
import { ProfilesDialogHost, type ProfilesDialogState } from "./profiles-dialog-host";

export function ProfilesModeSection() {
  const { t } = useTranslation("runtimeConfig");
  // D29 工作区信任上下文（Batch 14 slice 5）：本页没有会话上下文，工作区路径
  // 与 HarnessSettingsPage 同源（runtimeClient.workspacePath）。为空时列表请求
  // 不带 workspace 参数，后端也不回信任上下文 → 不渲染信任提示（旧行为零变化）。
  const runtimeClient = useRuntimeClientIdentity();
  const workspacePath = runtimeClient.workspacePath.trim();
  const [entries, setEntries] = useState<RuntimeProfileListEntry[]>([]);
  const [defaultProfile, setDefaultProfile] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [view, setView] = useState<RuntimeProfileView | null>(null);
  const [isViewLoading, setIsViewLoading] = useState(false);
  const [dialog, setDialog] = useState<ProfilesDialogState | null>(null);
  /** 列表响应回显的信任上下文；null = 尚未拿到响应。 */
  const [trust, setTrust] = useState<{
    workspacePath: string;
    trusted: boolean;
    featureEnabled: boolean;
  } | null>(null);
  const [isGrantingTrust, setIsGrantingTrust] = useState(false);

  const refresh = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      const result = await listRuntimeProfiles({ workspace: workspacePath });
      setEntries(result.profiles);
      setDefaultProfile(result.defaultProfile);
      setTrust({
        workspacePath: result.workspacePath,
        trusted: result.workspaceTrusted,
        featureEnabled: result.workspaceTrustFeatureEnabled,
      });
    } catch (loadError) {
      setError(formatProfileError(loadError, t("profiles.list.loadFailed")));
    } finally {
      setIsLoading(false);
    }
  }, [t, workspacePath]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  /**
   * Q22 一键信任：显式确认（ProfilesTrustNotice 内的两步）之后才发请求。
   * 成功后立即刷新列表——徽标与提示条都来自这次响应，不靠本地状态猜。
   */
  const runGrantTrust = useCallback(async () => {
    if (!workspacePath) {
      return;
    }
    setIsGrantingTrust(true);
    setError(null);
    setStatusMessage(null);
    try {
      await grantHarnessTrust(workspacePath);
      await refresh();
      setStatusMessage(t("profiles.trust.granted"));
    } catch (grantError) {
      setError(formatProfileError(grantError, t("profiles.trust.failed")));
    } finally {
      setIsGrantingTrust(false);
    }
  }, [refresh, t, workspacePath]);

  const openEditor = useCallback(
    async (entry: RuntimeProfileListEntry) => {
      setError(null);
      setStatusMessage(null);
      setIsViewLoading(true);
      try {
        setView(await getRuntimeProfile(entry.ref));
      } catch (loadError) {
        setError(formatProfileError(loadError, t("profiles.messages.loadFailed")));
      } finally {
        setIsViewLoading(false);
      }
    },
    [t],
  );

  /** 保存成功后同步列表行与编辑器 view，不整页重载。 */
  const handleSaved = useCallback((next: RuntimeProfileView) => {
    setView(next);
    setEntries((current) =>
      current.map((entry) => (entry.ref === next.ref ? mergeProfileView(entry, next) : entry)),
    );
  }, []);

  // 本页（/runtime-config）没有会话上下文，而 `/apply` 必须显式给出 session_id
  // （服务端不推断「当前会话」，A12）——所以这里不提供直接切换：行上的 apply 按钮
  // 保持可见但禁用，并把用户指向会话内的 composer `/profile`（Batch 12，同一执行
  // 核心，下一轮生效）。「设为默认」仍在页内可用（只影响新会话，D26）。

  const runSetDefault = useCallback(
    async (entry: RuntimeProfileListEntry) => {
      setPending(`default:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const result = await setDefaultRuntimeProfile(entry.ref);
        setDefaultProfile(result.defaultProfile);
        setEntries((current) =>
          current.map((item) => ({
            ...item,
            isDefault:
              item.name === result.defaultProfile || item.ref === result.defaultProfile,
          })),
        );
        setStatusMessage(t("profiles.list.defaultSucceeded", { name: entry.name }));
      } catch (defaultError) {
        setError(formatProfileError(defaultError, t("profiles.list.defaultFailed")));
      } finally {
        setPending(null);
      }
    },
    [t],
  );

  const openReferences = useCallback(
    async (entry: RuntimeProfileListEntry) => {
      setError(null);
      setDialog({ kind: "references", entry, isLoading: true, references: [] });
      try {
        const result = await listRuntimeProfileReferences(entry.ref);
        setDialog({
          kind: "references",
          entry,
          isLoading: false,
          references: profileReferenceItems(result),
        });
      } catch (referencesError) {
        setDialog(null);
        setError(
          formatProfileError(referencesError, t("profiles.messages.referencesLoadFailed")),
        );
      }
    },
    [t],
  );

  const runCreate = useCallback(
    async (request: RuntimeProfileCreateRequest) => {
      setPending("create");
      setError(null);
      setStatusMessage(null);
      try {
        await createRuntimeProfile(request);
        setDialog(null);
        setStatusMessage(t("profiles.lifecycle.succeeded"));
        await refresh();
      } catch (createError) {
        setError(formatProfileError(createError, t("profiles.create.failed")));
      } finally {
        setPending(null);
      }
    },
    [refresh, t],
  );

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
    [t],
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
    [t],
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
    [t],
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
    [t],
  );

  const runExport = useCallback(
    async (entry: RuntimeProfileListEntry) => {
      setPending(`export:${entry.ref}`);
      setError(null);
      setStatusMessage(null);
      try {
        const bundle = await exportRuntimeProfile(entry.ref);
        downloadProfileBundle(bundle);
        setStatusMessage(
          t("profiles.transfer.exportSucceeded", { name: entry.name, count: bundle.fileCount }),
        );
      } catch (exportError) {
        setError(formatProfileError(exportError, t("profiles.transfer.exportFailed")));
      } finally {
        setPending(null);
      }
    },
    [t],
  );

  const runImportPreview = useCallback(
    async (bundle: File, name: string, layer: string) => {
      setDialog((current) =>
        current && current.kind === "import" ? { ...current, isPreviewing: true } : current,
      );
      setError(null);
      try {
        const report = await importRuntimeProfile(bundle, {
          name: name || undefined,
          layer,
          dryRun: true,
        });
        setDialog({ kind: "import", isPreviewing: false, preview: report });
      } catch (previewError) {
        setDialog((current) =>
          current && current.kind === "import" ? { ...current, isPreviewing: false } : current,
        );
        setError(formatProfileError(previewError, t("profiles.transfer.importFailed")));
      }
    },
    [t],
  );

  const runImport = useCallback(
    async (bundle: File, name: string, layer: string) => {
      setPending("import");
      setError(null);
      setStatusMessage(null);
      try {
        const report = await importRuntimeProfile(bundle, { name: name || undefined, layer });
        if (!report.imported) {
          // 领域拒绝（D28-1：包过不了同一个 validate）：把问题清单留在对话框里，
          // 不关对话框、不假装成功；此时后端未创建目标目录。
          setDialog({ kind: "import", isPreviewing: false, preview: report });
          setError(report.error || t("profiles.transfer.importFailed"));
          return;
        }
        setDialog(null);
        setStatusMessage(t("profiles.transfer.importSucceeded", { name: report.name }));
        await refresh();
      } catch (importError) {
        setError(formatProfileError(importError, t("profiles.transfer.importFailed")));
      } finally {
        setPending(null);
      }
    },
    [refresh, t],
  );

  const filtered = useMemo(() => filterProfileEntries(entries, filter), [entries, filter]);
  const errorCount = entries.filter((entry) => !entry.valid).length;
  const busy = pending !== null;

  return (
    <>
      <ProfileListHeader
        busy={busy}
        count={entries.length}
        defaultProfile={defaultProfile}
        errorCount={errorCount}
        filter={filter}
        isLoading={isLoading}
        onCreate={() => {
          setError(null);
          setStatusMessage(null);
          setDialog({ kind: "create" });
        }}
        onFilterChange={setFilter}
        onImport={() => {
          setError(null);
          setStatusMessage(null);
          setDialog({ kind: "import", isPreviewing: false, preview: null });
        }}
        onRefresh={() => {
          void refresh();
        }}
      />

      {trust && trust.workspacePath && trust.featureEnabled && !trust.trusted ? (
        <ProfilesTrustNotice
          granting={isGrantingTrust}
          workspacePath={trust.workspacePath}
          onGrant={() => {
            void runGrantTrust();
          }}
        />
      ) : null}

      {statusMessage ? (
        <SettingsNoticeCard className="mt-0" tone="neutral">
          <span data-testid="profiles-status" role="status">
            {statusMessage}
          </span>
        </SettingsNoticeCard>
      ) : null}

      {error ? (
        <SettingsNoticeCard className="mt-0" tone="warning">
          <span data-testid="profiles-error" role="alert">
            {error}
          </span>
        </SettingsNoticeCard>
      ) : null}

      {isViewLoading ? (
        <SettingsEmptyState variant="dashed">{t("profiles.list.loading")}</SettingsEmptyState>
      ) : view ? (
        <ProfileEditor
          view={view}
          onClose={() => {
            setView(null);
          }}
          onSaved={handleSaved}
        />
      ) : null}

      {isLoading ? (
        <SettingsEmptyState variant="dashed">{t("profiles.list.loading")}</SettingsEmptyState>
      ) : filtered.length === 0 ? (
        <SettingsEmptyState variant="dashed">
          {entries.length === 0 ? t("profiles.list.empty") : t("profiles.list.emptyFiltered")}
        </SettingsEmptyState>
      ) : (
        <div className="grid gap-2" data-testid="profiles-list">
          {filtered.map((entry) => (
            <ProfileListRow
              key={entry.ref}
              applyDisabled
              busy={busy}
              entry={entry}
              isDefaultTarget={entry.name === defaultProfile || entry.ref === defaultProfile}
              selected={view?.ref === entry.ref}
              onDelete={(item) => {
                setDialog({ kind: "delete", entry: item, referenceCount: 0 });
              }}
              onDuplicate={(item) => {
                setDialog({ kind: "duplicate", entry: item });
              }}
              onExport={(item) => {
                void runExport(item);
              }}
              onMove={(item) => {
                setDialog({ kind: "move", entry: item });
              }}
              onOpen={(item) => {
                void openEditor(item);
              }}
              onRename={(item) => {
                setDialog({ kind: "rename", entry: item });
              }}
              onSetDefault={(item) => {
                void runSetDefault(item);
              }}
              onShowReferences={(item) => {
                void openReferences(item);
              }}
            />
          ))}
        </div>
      )}

      <ProfilesDialogHost
        dialog={dialog}
        entries={entries}
        pending={pending}
        onClose={() => {
          setDialog(null);
        }}
        onCreate={(request) => {
          void runCreate(request);
        }}
        onDelete={(entry, force) => {
          void runDelete(entry, force);
        }}
        onDuplicate={(entry, name, layer) => {
          void runDuplicate(entry, name, layer);
        }}
        onImportPreview={(bundle, name, layer) => {
          void runImportPreview(bundle, name, layer);
        }}
        onImportSubmit={(bundle, name, layer) => {
          void runImport(bundle, name, layer);
        }}
        onMove={(entry, layer) => {
          void runMove(entry, layer);
        }}
        onRename={(entry, name) => {
          void runRename(entry, name);
        }}
      />
    </>
  );
}
