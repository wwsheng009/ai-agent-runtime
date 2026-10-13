// 连接器配置页（/runtime/connectors）：列表 + 新建/编辑 + 测试消息 + 启停/重载/删除。
//
// 数据面：GET/POST /api/connectors、PATCH/DELETE /api/connectors/{id}、
// POST /{id}/reload、POST /{id}/test（见 @/api/runtime/connectors）。
// 表单渲染完全由 Schema 驱动（docs/bots/07-config-ux.md §7），页面不硬编码通道字段。

import { ArrowLeftIcon, BotIcon, PlusIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import {
  createConnector,
  deleteConnector,
  getConnectorSchema,
  listConnectors,
  readConnectorFieldErrors,
  reloadConnector,
  updateConnector,
} from "@/api/runtime/connectors";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { readAdminToken } from "@/lib/admin-token";
import { cn } from "@/lib/utils";
import type {
  ConnectorFieldError,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import { ConnectorDialog } from "./connectors/connector-dialog";
import { TestMessageDialog } from "./connectors/test-message-dialog";

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function ConnectorsPage() {
  const { t } = useTranslation("connectors");
  const adminToken = readAdminToken();

  const [schema, setSchema] = useState<ConnectorSchema | null>(null);
  const [items, setItems] = useState<ConnectorView[]>([]);
  const [loading, setLoading] = useState(true);
  const [schemaError, setSchemaError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  /** 正在执行行内操作（启停/重载/删除）的连接器 id 集合；操作期间同时禁用该条全部按钮。 */
  const [busyIds, setBusyIds] = useState<Set<string>>(() => new Set());

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<ConnectorView | null>(null);
  const [saving, setSaving] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [dialogFieldErrors, setDialogFieldErrors] = useState<ConnectorFieldError[]>([]);
  const [testTarget, setTestTarget] = useState<ConnectorView | null>(null);

  const reloadData = useCallback(async () => {
    setLoading(true);
    setSchemaError(null);
    setLoadError(null);
    const [schemaResult, listResult] = await Promise.allSettled([
      getConnectorSchema({ adminToken }),
      listConnectors({ adminToken }),
    ]);
    setSchema(schemaResult.status === "fulfilled" ? schemaResult.value : null);
    setItems(listResult.status === "fulfilled" ? listResult.value : []);
    setSchemaError(
      schemaResult.status === "rejected" ? errorText(schemaResult.reason) : null,
    );
    setLoadError(
      listResult.status === "rejected" ? errorText(listResult.reason) : null,
    );
    setLoading(false);
  }, [adminToken]);

  useEffect(() => {
    void reloadData();
  }, [reloadData]);

  const openCreate = () => {
    setEditing(null);
    setDialogError(null);
    setDialogFieldErrors([]);
    setDialogOpen(true);
  };

  const openEdit = (item: ConnectorView) => {
    setEditing(item);
    setDialogError(null);
    setDialogFieldErrors([]);
    setDialogOpen(true);
  };

  const closeDialog = () => {
    setDialogOpen(false);
    setEditing(null);
    setSaving(false);
    setDialogError(null);
    setDialogFieldErrors([]);
  };

  const handleSave = async (payload: ConnectorSubmitPayload) => {
    setSaving(true);
    setDialogError(null);
    setDialogFieldErrors([]);
    try {
      if (editing) {
        await updateConnector(editing.id, payload, { adminToken });
      } else {
        await createConnector(payload, { adminToken });
      }
      closeDialog();
      await reloadData();
    } catch (caught) {
      setDialogFieldErrors(readConnectorFieldErrors(caught));
      setDialogError(errorText(caught));
      setSaving(false);
    }
  };

  const markBusy = (id: string) => {
    setBusyIds((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
  };

  const clearBusy = (id: string) => {
    setBusyIds((prev) => {
      if (!prev.has(id)) {
        return prev;
      }
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  };

  const runAction = async (
    item: ConnectorView,
    action: () => Promise<unknown>,
    failureKey: "page.toggleFailed" | "page.reloadFailed" | "page.removeFailed",
  ) => {
    markBusy(item.id);
    setNotice(null);
    try {
      await action();
      await reloadData();
    } catch (caught) {
      setNotice(t(failureKey, { message: errorText(caught) }));
    } finally {
      clearBusy(item.id);
    }
  };

  const handleToggle = (item: ConnectorView) =>
    runAction(
      item,
      () => updateConnector(item.id, { enabled: !item.enabled }, { adminToken }),
      "page.toggleFailed",
    );

  const handleReload = (item: ConnectorView) =>
    runAction(
      item,
      async () => {
        await reloadConnector(item.id, { adminToken });
        setNotice(t("page.reloaded", { name: item.name }));
      },
      "page.reloadFailed",
    );

  const handleDelete = (item: ConnectorView) => {
    if (!window.confirm(t("page.confirmDelete", { name: item.name }))) {
      return;
    }
    return runAction(
      item,
      async () => {
        await deleteConnector(item.id, { adminToken });
        setNotice(t("page.removed", { name: item.name }));
      },
      "page.removeFailed",
    );
  };

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0">
        <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3.5 py-3">
          <div className="relative flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                  <BotIcon size={13} />
                  {t("page.badge")}
                </Badge>
                <Badge>{t("page.independentPage")}</Badge>
              </div>
              <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                {t("page.title")}
              </h1>
            </div>

            <nav className="flex flex-wrap items-center gap-2" aria-label={t("page.title")}>
              <Link
                to="/workspace/chats/new"
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                <ArrowLeftIcon size={14} />
                {t("page.backToWorkspace")}
              </Link>
              <Link
                to="/runtime/config"
                className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
              >
                {t("page.runtimeConfig")}
              </Link>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void reloadData()}
                disabled={loading}
              >
                <RefreshCwIcon size={14} />
                {t("page.refresh")}
              </Button>
              <Button
                variant="primary"
                size="sm"
                onClick={openCreate}
                disabled={!schema || schema.providers.length === 0}
              >
                <PlusIcon size={14} />
                {t("page.create")}
              </Button>
            </nav>
          </div>
        </header>

        <section className="surface-panel min-h-0 rounded-panel-lg px-3 py-3.5 sm:px-4 lg:flex-1 lg:overflow-y-auto">
          {notice ? (
            <div className="mb-3 rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {notice}
            </div>
          ) : null}
          {loadError ? (
            <div className="mb-3 rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("page.loadFailed", { message: loadError })}
            </div>
          ) : null}
          {schemaError ? (
            <div className="mb-3 rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("page.schemaFailed", { message: schemaError })}
            </div>
          ) : null}

          {loading ? (
            <p className="text-sm text-muted-foreground">{t("page.loading")}</p>
          ) : items.length === 0 && !loadError ? (
            <p className="text-sm text-muted-foreground">{t("page.empty")}</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {items.map((item) => (
                <li
                  key={item.id}
                  className="flex flex-col gap-2 rounded-card border border-border bg-surface-soft px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div className="flex min-w-0 flex-col gap-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-sm font-medium text-foreground">
                        {item.name}
                      </span>
                      <Badge>{item.provider}</Badge>
                      {item.mode ? <Badge>{item.mode}</Badge> : null}
                      <Badge>{item.enabled ? t("page.enabled") : t("page.disabled")}</Badge>
                      {item.policy.runtime_owner ? (
                        <span className="text-xs text-muted-foreground">
                          {t("page.owner", { owner: item.policy.runtime_owner })}
                        </span>
                      ) : null}
                    </div>
                    <span className="truncate text-xs text-muted-foreground">{item.id}</span>
                  </div>

                  <div className="flex flex-wrap items-center gap-1.5">
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={busyIds.has(item.id)}
                      onClick={() => void handleToggle(item)}
                    >
                      {item.enabled ? t("page.disable") : t("page.enable")}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={!schema || busyIds.has(item.id)}
                      onClick={() => openEdit(item)}
                    >
                      {t("page.edit")}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={busyIds.has(item.id)}
                      onClick={() => setTestTarget(item)}
                    >
                      {t("page.test")}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={busyIds.has(item.id)}
                      onClick={() => void handleReload(item)}
                    >
                      {t("page.reload")}
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={busyIds.has(item.id)}
                      onClick={() => void handleDelete(item)}
                    >
                      {t("page.remove")}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <ConnectorDialog
        open={dialogOpen}
        schema={schema}
        existing={editing}
        saving={saving}
        serverError={dialogError}
        serverFieldErrors={dialogFieldErrors}
        onSubmit={(payload) => void handleSave(payload)}
        onClose={closeDialog}
      />
      <TestMessageDialog
        open={testTarget !== null}
        connector={testTarget}
        onClose={() => setTestTarget(null)}
      />
    </div>
  );
}
