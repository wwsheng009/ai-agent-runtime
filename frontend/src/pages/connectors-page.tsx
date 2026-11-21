// 连接器配置页（/runtime/connectors）：列表 + 新建/编辑 + 测试消息 + 启停/重载/删除。
//
// 数据面：GET/POST /api/connectors、PATCH/DELETE /api/connectors/{id}、
// POST /{id}/reload、POST /{id}/test（见 @/api/runtime/connectors）。
// 表单渲染完全由 Schema 驱动（docs/bots/07-config-ux.md §7），页面不硬编码通道字段。

import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  BotIcon,
  CheckCircle2Icon,
  PlusIcon,
  RefreshCwIcon,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import {
  createConnector,
  deleteConnector,
  getConnectorSchema,
  listConnectorBindings,
  listConnectors,
  readConnectorFieldErrors,
  reloadConnector,
  runConnectorAction,
  takeoverConnector,
  updateConnector,
} from "@/api/runtime/connectors";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { readAdminToken } from "@/lib/admin-token";
import { cn } from "@/lib/utils";
import type {
  ConnectorActionSpec,
  ConnectorBindingView,
  ConnectorFieldError,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import { summarizeActionPayload } from "./connectors/action-payload";
import { ConnectorActionDialog } from "./connectors/action-dialog";
import { ConnectorDialog } from "./connectors/connector-dialog";
import { ConnectorLogsDialog } from "./connectors/logs-dialog";
import { ConnectorRow } from "./connectors/connector-row";
import { TestMessageDialog } from "./connectors/test-message-dialog";

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** 二维码类动作的目标：打开扫码登录弹窗。 */
interface ConnectorActionTarget {
  connector: ConnectorView;
  action: ConnectorActionSpec;
}

interface PageNotice {
  kind: "success" | "error";
  message: string;
}

export function ConnectorsPage({ embedded = false }: { embedded?: boolean } = {}) {
  const { t } = useTranslation("connectors");
  const adminToken = readAdminToken();

  const [schema, setSchema] = useState<ConnectorSchema | null>(null);
  const [items, setItems] = useState<ConnectorView[]>([]);
  const [bindings, setBindings] = useState<ConnectorBindingView[]>([]);
  const [loading, setLoading] = useState(true);
  const [schemaError, setSchemaError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [notice, setNotice] = useState<PageNotice | null>(null);
  /** 正在执行行内操作（启停/重载/删除）的连接器 id 集合；操作期间同时禁用该条全部按钮。 */
  const [busyIds, setBusyIds] = useState<Set<string>>(() => new Set());

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<ConnectorView | null>(null);
  const [saving, setSaving] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [dialogFieldErrors, setDialogFieldErrors] = useState<ConnectorFieldError[]>([]);
  const [testTarget, setTestTarget] = useState<ConnectorView | null>(null);
  const [actionTarget, setActionTarget] = useState<ConnectorActionTarget | null>(null);
  const [logsTarget, setLogsTarget] = useState<ConnectorView | null>(null);

  const reloadData = useCallback(async () => {
    setLoading(true);
    setSchemaError(null);
    setLoadError(null);
    const [schemaResult, listResult, bindingsResult] = await Promise.allSettled([
      getConnectorSchema({ adminToken }),
      listConnectors({ adminToken }),
      listConnectorBindings({ adminToken }),
    ]);
    setSchema(schemaResult.status === "fulfilled" ? schemaResult.value : null);
    setItems(listResult.status === "fulfilled" ? listResult.value : []);
    // 绑定是「所在会话与节点」展示的 additive 数据面：加载失败静默降级为不展示
    // （不阻塞连接器主列表与编辑），下次刷新自动恢复。
    setBindings(
      bindingsResult.status === "fulfilled" ? bindingsResult.value ?? [] : [],
    );
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

  /** 该连接器的绑定列表（chat → 会话 + 节点，docs/bots/09 §5.3）。 */
  const bindingsOf = (connectorId: string) =>
    bindings.filter((binding) => binding.connector_id === connectorId);

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
    describeFailure: (message: string) => string,
  ) => {
    markBusy(item.id);
    setNotice(null);
    try {
      await action();
      await reloadData();
    } catch (caught) {
      setNotice({
        kind: "error",
        message: describeFailure(errorText(caught)),
      });
    } finally {
      clearBusy(item.id);
    }
  };

  const handleToggle = (item: ConnectorView) =>
    runAction(
      item,
      () => updateConnector(item.id, { enabled: !item.enabled }, { adminToken }),
      (message) => t("page.toggleFailed", { message }),
    );

  const handleReload = (item: ConnectorView) =>
    runAction(
      item,
      async () => {
        await reloadConnector(item.id, { adminToken });
        setNotice({
          kind: "success",
          message: t("page.reloaded", { name: item.name }),
        });
      },
      (message) => t("page.reloadFailed", { message }),
    );

  const handleDelete = (item: ConnectorView) => {
    if (!window.confirm(t("page.confirmDelete", { name: item.name }))) {
      return;
    }
    return runAction(
      item,
      async () => {
        await deleteConnector(item.id, { adminToken });
        setNotice({
          kind: "success",
          message: t("page.removed", { name: item.name }),
        });
      },
      (message) => t("page.removeFailed", { message }),
    );
  };

  /** 抢占运行归属：仅当启用且归属在别的宿主时展示（二次确认）。 */
  const handleTakeover = (item: ConnectorView) => {
    const owner = item.active_owner ?? "";
    if (!owner || item.owned_by_current_host) {
      return;
    }
    if (!window.confirm(t("page.confirmTakeover", { name: item.name, owner }))) {
      return;
    }
    return runAction(
      item,
      async () => {
        await takeoverConnector(item.id, { adminToken });
        setNotice({
          kind: "success",
          message: t("page.takeoverDone", { name: item.name }),
        });
      },
      (message) => t("page.takeoverFailed", { message }),
    );
  };

  /** 通道动作：qr 类走扫码登录弹窗，其余（button）直接执行并展示返回/错误。 */
  const handleRunAction = (item: ConnectorView, action: ConnectorActionSpec) => {
    if (action.kind === "qr") {
      setActionTarget({ connector: item, action });
      return;
    }
    return runAction(
      item,
      async () => {
        const payload = await runConnectorAction(item.id, action.key, undefined, {
          adminToken,
        });
        const result = summarizeActionPayload(payload);
        setNotice(
          result
            ? {
                kind: "success",
                message: t("page.actionResult", { action: action.label, result }),
              }
            : {
                kind: "success",
                message: t("page.actionResultEmpty", { action: action.label }),
              },
        );
      },
      (message) => t("page.actionFailed", { action: action.label, message }),
    );
  };

  const handleRefilled = useCallback(() => {
    void reloadData();
  }, [reloadData]);
  return (
    <div
      className={
        embedded
          ? "space-y-3 text-foreground"
          : "min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden"
      }
    >
      <div
        className={
          embedded
            ? "space-y-3"
            : "mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0"
        }
      >
        <header
          className={
            embedded
              ? "rounded-panel border border-border bg-surface-softer px-3 py-3"
              : "surface-panel relative overflow-hidden rounded-panel-lg px-3.5 py-3"
          }
        >
          <div className="relative flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                  <BotIcon size={13} />
                  {t("page.badge")}
                </Badge>
                {!embedded ? <Badge>{t("page.independentPage")}</Badge> : null}
              </div>
              {!embedded ? (
                <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                  {t("page.title")}
                </h1>
              ) : null}
            </div>

            <nav className="flex flex-wrap items-center gap-2" aria-label={t("page.title")}>
              {!embedded ? (
                <>
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
                </>
              ) : null}
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

        <section
          className={
            embedded
              ? "min-h-0 rounded-panel border border-border bg-surface-softer px-3 py-3.5 sm:px-4"
              : "surface-panel min-h-0 rounded-panel-lg px-3 py-3.5 sm:px-4 lg:flex-1 lg:overflow-y-auto"
          }
        >
          {notice ? (
            <div
              role={notice.kind === "success" ? "status" : "alert"}
              className={cn(
                "mb-3 flex items-start gap-2 rounded-card border px-3 py-2 text-sm",
                notice.kind === "success"
                  ? "border-analytics-success-border bg-analytics-success-soft text-analytics-success"
                  : "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger",
              )}
            >
              {notice.kind === "success" ? (
                <CheckCircle2Icon size={16} className="mt-0.5 shrink-0" aria-hidden="true" />
              ) : (
                <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" aria-hidden="true" />
              )}
              <span className="min-w-0 break-words">{notice.message}</span>
            </div>
          ) : null}
          {loadError ? (
            <div
              role="alert"
              className="mb-3 rounded-card border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2 text-sm text-analytics-danger"
            >
              {t("page.loadFailed", { message: loadError })}
            </div>
          ) : null}
          {schemaError ? (
            <div
              role="alert"
              className="mb-3 rounded-card border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2 text-sm text-analytics-danger"
            >
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
                <ConnectorRow
                  key={item.id}
                  item={item}
                  schema={schema}
                  bindings={bindingsOf(item.id)}
                  busy={busyIds.has(item.id)}
                  onRunAction={handleRunAction}
                  onSetLogs={setLogsTarget}
                  onToggle={(next) => void handleToggle(next)}
                  onEdit={openEdit}
                  onSetTest={setTestTarget}
                  onTakeover={(next) => void handleTakeover(next)}
                  onReload={(next) => void handleReload(next)}
                  onDelete={(next) => void handleDelete(next)}
                />
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
        onChanged={handleRefilled}
        onClose={closeDialog}
      />
      <TestMessageDialog
        open={testTarget !== null}
        connector={testTarget}
        onClose={() => setTestTarget(null)}
      />
      <ConnectorActionDialog
        open={actionTarget !== null}
        connector={actionTarget?.connector ?? null}
        action={actionTarget?.action ?? null}
        onRefilled={handleRefilled}
        onClose={() => setActionTarget(null)}
      />
      <ConnectorLogsDialog
        open={logsTarget !== null}
        connector={logsTarget}
        onClose={() => setLogsTarget(null)}
      />
    </div>
  );
}
