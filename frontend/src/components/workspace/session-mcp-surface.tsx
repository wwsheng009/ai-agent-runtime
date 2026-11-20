// 工作台右侧栏「会话 MCP」面：按会话查看 MCP 服务的全局状态，并提供
// 「本会话停用 / 恢复」开关（语义与后端 /api/runtime/sessions/{id}/runtime/mcps 对齐）。
//
// 契约：
//   * 自包含面（panel-registry 注册 `surface`），只接收会话上下文 props；
//   * 只调用 runtime MCP 客户端（listRuntimeMcps + listRuntimeSessionMcps +
//     setRuntimeSessionMcpEnabled），不直接拼 URL；
//   * 停用只影响本会话（不写配置文件），后端 message 原样展示；
//   * 全局停用的 server 不提供会话级启用入口（后端 409：暂无临时连接），
//     入口禁用并给出解释，不制造必然失败的操作。
//
// 刷新触发：会话切换、手动刷新、`mcp.*` 运行时事件（面板宿主透传的计数变化）。

import {
  AlertTriangleIcon,
  PlusIcon,
  RefreshCwIcon,
  ServerIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createRuntimeWorkspaceMcp,
  createRuntimeSessionMcp,
  deleteRuntimeWorkspaceMcp,
  deleteRuntimeSessionMcp,
  listRuntimeMcps,
  listRuntimeSessionMcps,
  listRuntimeWorkspaceMcps,
  setRuntimeWorkspaceMcpEnabled,
  setRuntimeSessionMcpEnabled,
} from "@/api/runtime/mcp";
import { type WorkspacePanelSurfaceProps } from "@/components/workspace/panel-registry";
import { SessionMcpRows } from "@/components/workspace/session-mcp-rows";
import { SessionMcpScopeCard } from "@/components/workspace/session-mcp-scope-card";
import { SessionMcpToolsDialog } from "@/components/workspace/session-mcp-tools-dialog";
import {
  SESSION_DETAIL_CARD_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import {
  buildMcpUpsertRequest,
  createMcpDraft,
  RuntimeMcpForm,
  type McpDraft,
  type McpValidationError,
} from "@/components/workspace/settings/backend-config-settings-page/sections/modes/mcp-form";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  type RuntimeSessionMcpEntry,
  type RuntimeSessionMcpScope,
  type RuntimeWorkspaceMcpListResponse,
} from "@/types/runtime";

type LoadStatus = "loading" | "ready" | "error";

const VALIDATION_MESSAGE_KEYS = {
  name: "panels.sessionMcp.validationNameRequired",
  command: "panels.sessionMcp.validationCommandRequired",
  url: "panels.sessionMcp.validationUrlRequired",
  timeoutSeconds: "panels.sessionMcp.validationTimeoutInvalid",
  maxParallelCalls: "panels.sessionMcp.validationMaxParallelCallsInvalid",
  duplicateKey: "panels.sessionMcp.validationDuplicateKey",
} as const satisfies Record<McpValidationError, string>;

function formatMcpError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function sessionScopeFromWorkspace(
  response: RuntimeWorkspaceMcpListResponse,
): RuntimeSessionMcpScope {
  return {
    workspace: response.workspace_path,
    workspace_scoped: true,
    workspace_fallback: response.workspace_fallback,
    read: response.read,
    write: response.write,
    candidates: response.candidates,
  };
}

export function SessionMcpSurface({
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
  workspacePath,
}: WorkspacePanelSurfaceProps) {
  const { t } = useTranslation("workspace");
  const [entries, setEntries] = useState<RuntimeSessionMcpEntry[]>([]);
  const [disabledNames, setDisabledNames] = useState<string[]>([]);
  /** 「本会话临时启用」名单（配置停用 + 会话私有内存连接）。 */
  const [tempEnabledNames, setTempEnabledNames] = useState<string[]>([]);
  /** 会话生效的配置面（新后端返回；旧后端缺省 → 隐藏持久化管理入口）。 */
  const [scope, setScope] = useState<RuntimeSessionMcpScope | null>(null);
  const [status, setStatus] = useState<LoadStatus>("loading");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [pendingName, setPendingName] = useState("");
  const [draft, setDraft] = useState<McpDraft | null>(null);
  /** 工具列表弹窗的目标 server；null = 关闭。 */
  const [toolsFor, setToolsFor] = useState<string | null>(null);
  const normalizedSessionId = sessionId.trim();
  const normalizedWorkspacePath = workspacePath?.trim() ?? "";
  const hasSession = normalizedSessionId !== "";
  const hasWorkspace = normalizedWorkspacePath !== "";

  const disabledSet = useMemo(() => new Set(disabledNames), [disabledNames]);
  const tempEnabledSet = useMemo(
    () => new Set(tempEnabledNames),
    [tempEnabledNames],
  );

  const refresh = useCallback(async () => {
    const normalizedSessionId = sessionId.trim();
    const normalizedWorkspacePath = workspacePath?.trim() ?? "";
    setEntries([]);
    setScope(null);
    setStatus("loading");
    setError("");

    if (normalizedWorkspacePath && !normalizedSessionId) {
      try {
        const workspace = await listRuntimeWorkspaceMcps(normalizedWorkspacePath);
        setDisabledNames([]);
        setTempEnabledNames([]);
        setEntries(workspace.mcps);
        setScope(sessionScopeFromWorkspace(workspace));
        setStatus("ready");
      } catch (loadError) {
        setDisabledNames([]);
        setTempEnabledNames([]);
        setError(formatMcpError(loadError));
        setStatus("error");
      }
      return;
    }

    if (normalizedWorkspacePath && normalizedSessionId) {
      try {
        const [workspace, scoped] = await Promise.all([
          listRuntimeWorkspaceMcps(normalizedWorkspacePath),
          listRuntimeSessionMcps(normalizedSessionId),
        ]);
        setDisabledNames(scoped.disabled);
        setTempEnabledNames(scoped.enabled ?? []);
        setScope(sessionScopeFromWorkspace(workspace));

        const sessionEntries = scoped.mcps ?? [];
        const sessionEntriesByName = new Map(
          sessionEntries.map((entry) => [
            entry.config?.name?.trim() || entry.status?.name?.trim() || "",
            entry,
          ]),
        );
        const workspaceNames = new Set(
          workspace.mcps
            .map((entry) => entry.config?.name?.trim() ?? "")
            .filter(Boolean),
        );
        const workspaceEntries = workspace.mcps.map((entry) => {
          const name = entry.config?.name?.trim() ?? "";
          const sessionEntry = sessionEntriesByName.get(name);
          return {
            ...entry,
            ...sessionEntry,
            source: "workspace" as const,
          };
        });
        // The session API may expose process-global entries while the workspace
        // has no local file. Keep them visible for session-only controls, but
        // mark them global so workspace persistence actions stay unavailable.
        const globalFallbackEntries = sessionEntries.filter((entry) => {
          const name = entry.config?.name?.trim() || entry.status?.name?.trim() || "";
          return entry.source === "global" && !workspaceNames.has(name);
        });
        setEntries([...workspaceEntries, ...globalFallbackEntries]);
        setStatus("ready");
      } catch (loadError) {
        setDisabledNames([]);
        setTempEnabledNames([]);
        setError(formatMcpError(loadError));
        setStatus("error");
      }
      return;
    }

    if (!normalizedSessionId) {
      setDisabledNames([]);
      setTempEnabledNames([]);
      setStatus("ready");
      return;
    }

    try {
      const scoped = await listRuntimeSessionMcps(normalizedSessionId);
      setDisabledNames(scoped.disabled);
      setTempEnabledNames(scoped.enabled ?? []);
      setScope(scoped.scope ?? null);
      if (scoped.mcps) {
        // 新后端：条目即会话实际生效的配置面（工作区锚定）。
        setEntries(scoped.mcps);
      } else {
        // 旧后端只返回覆盖清单：回退全局列表，行为与旧版一致。
        const global = await listRuntimeMcps();
        setEntries(global.mcps);
      }
      setStatus("ready");
    } catch (loadError) {
      setError(formatMcpError(loadError));
      setStatus("error");
    }
  }, [sessionId, workspacePath]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // `mcp.*` 运行时事件 → 重载；首帧只记录基线，不额外触发一次请求。
  const lastEventRef = useRef<{ count?: number }>({
    count: runtimeEventCount,
  });
  useEffect(() => {
    const previousCount = lastEventRef.current.count;
    lastEventRef.current = { count: runtimeEventCount };
    if (runtimeEventCount === undefined || previousCount === undefined) {
      return;
    }
    if (runtimeEventCount === previousCount) {
      return;
    }
    if (!(lastRuntimeEventType ?? "").startsWith("mcp")) {
      return;
    }
    void refresh();
  }, [lastRuntimeEventType, refresh, runtimeEventCount]);

  const handleToggle = useCallback(
    async (entry: RuntimeSessionMcpEntry) => {
      const name = entry.config?.name?.trim();
      if (!name || !sessionId.trim()) {
        return;
      }
      const currentlyDisabled = disabledSet.has(name);
      // 同上（rows 注释）：临时启用名单以本地集合为准，避免旧条目值遮蔽。
      const entryTempEnabled = tempEnabledSet.has(name);
      const effectiveEnabled =
        entry.status?.enabled ?? entry.config?.enabled ?? true;
      // 按钮语义（见 session-mcp-rows）：恢复覆盖 / 回收临时连接 → disable；
      // 临时启用（配置停用未启用）/ 常规 server → enable。
      const targetEnabled =
        currentlyDisabled || (!effectiveEnabled && !entryTempEnabled);
      setPendingName(name);
      setNotice("");
      setError("");
      try {
        const result = await setRuntimeSessionMcpEnabled(
          sessionId,
          name,
          targetEnabled,
        );
        const nowDisabled = result.session_state === "disabled";
        const nowTempEnabled = result.session_state === "enabled";
        setDisabledNames((previous) => {
          if (nowDisabled) {
            return Array.from(new Set([...previous, name])).sort();
          }
          return previous.filter((item) => item !== name);
        });
        setTempEnabledNames((previous) => {
          if (nowTempEnabled) {
            return Array.from(new Set([...previous, name])).sort();
          }
          return previous.filter((item) => item !== name);
        });
        setNotice(
          result.message ||
            t(
              targetEnabled
                ? "panels.sessionMcp.enableNotice"
                : "panels.sessionMcp.disableNotice",
              { name },
            ),
        );
      } catch (toggleError) {
        setError(formatMcpError(toggleError));
      } finally {
        setPendingName("");
      }
    },
    [disabledSet, tempEnabledSet, sessionId, t],
  );

  /** 持久化启停：写入会话生效的配置文件并热重载（影响共享该配置的会话）。 */
  const handlePersistToggle = useCallback(
    async (entry: RuntimeSessionMcpEntry) => {
      const name = entry.config?.name?.trim() || entry.status?.name?.trim();
      const workspace = workspacePath?.trim() ?? "";
      const normalizedSessionId = sessionId.trim();
      if (!name || (!workspace && !normalizedSessionId)) {
        return;
      }
      const enabled = entry.status?.enabled ?? entry.config?.enabled ?? true;
      const targetEnabled = !enabled;
      setPendingName(name);
      setNotice("");
      setError("");
      try {
        const result = workspace
          ? await setRuntimeWorkspaceMcpEnabled(
              workspace,
              name,
              targetEnabled,
            )
          : await setRuntimeSessionMcpEnabled(
              normalizedSessionId,
              name,
              targetEnabled,
              "workspace",
            );
        setNotice(
          result.message ||
            t(
              targetEnabled
                ? "panels.sessionMcp.persistEnabledNotice"
                : "panels.sessionMcp.persistDisabledNotice",
              { name },
            ),
        );
        await refresh();
      } catch (toggleError) {
        setError(formatMcpError(toggleError));
      } finally {
        setPendingName("");
      }
    },
    [refresh, sessionId, t, workspacePath],
  );

  /** 删除：从会话生效的配置文件中移除（写文件 + 热重载）。 */
  const handleDelete = useCallback(
    async (entry: RuntimeSessionMcpEntry) => {
      const name = entry.config?.name?.trim() || entry.status?.name?.trim();
      const workspace = workspacePath?.trim() ?? "";
      const normalizedSessionId = sessionId.trim();
      if (!name || (!workspace && !normalizedSessionId)) {
        return;
      }
      if (!window.confirm(t("panels.sessionMcp.deleteConfirm", { name }))) {
        return;
      }
      setPendingName(name);
      setNotice("");
      setError("");
      try {
        if (workspace) {
          await deleteRuntimeWorkspaceMcp(workspace, name);
        } else {
          await deleteRuntimeSessionMcp(normalizedSessionId, name);
        }
        setNotice(t("panels.sessionMcp.deleteSuccess", { name }));
        await refresh();
      } catch (deleteError) {
        setError(formatMcpError(deleteError));
      } finally {
        setPendingName("");
      }
    },
    [refresh, sessionId, t, workspacePath],
  );

  /** 新增：写入会话生效的配置文件（工作区锚定时即工作区 mcp.yaml）。 */
  const submitAdd = useCallback(async () => {
    const workspace = workspacePath?.trim() ?? "";
    const normalizedSessionId = sessionId.trim();
    if (!draft || (!workspace && !normalizedSessionId) || pendingName) {
      return;
    }
    const built = buildMcpUpsertRequest(draft);
    if ("validationError" in built) {
      setError(t(VALIDATION_MESSAGE_KEYS[built.validationError]));
      return;
    }
    const name = built.request.name;
    setPendingName(name);
    setError("");
    setNotice("");
    try {
      if (workspace) {
        await createRuntimeWorkspaceMcp(workspace, built.request);
      } else {
        await createRuntimeSessionMcp(normalizedSessionId, built.request);
      }
      setNotice(t("panels.sessionMcp.createSuccess", { name }));
      setDraft(null);
      await refresh();
    } catch (createError) {
      setError(formatMcpError(createError));
    } finally {
      setPendingName("");
    }
  }, [draft, pendingName, refresh, sessionId, t, workspacePath]);

  const loading = status === "loading";
  const loadFailed = status === "error";

  return (
    <section
      aria-label={t("panels.sessionMcp.ariaLabel")}
      className="flex min-h-0 flex-col gap-2.5 overflow-y-auto px-3 pb-4"
      data-testid="session-mcp-surface"
    >
      <header className="sticky top-0 z-10 -mx-3 flex items-start justify-between gap-2 border-b border-border/60 bg-surface-softer/90 px-3 pt-3 pb-2.5 backdrop-blur">
        <div className="flex min-w-0 items-start gap-2">
          <span
            aria-hidden="true"
            className="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-chip border border-border bg-surface-soft text-accent-primary"
          >
            <ServerIcon size={13} />
          </span>
          <div className="min-w-0">
            <h2 className="truncate text-xs font-semibold text-foreground">
              {t("panels.sessionMcp.title")}
            </h2>
            <p className="mt-0.5 app-text-11 leading-4 text-muted-foreground">
              {t("panels.sessionMcp.hint")}
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {scope ? (
            <Button
              aria-label={t("panels.sessionMcp.addAction")}
              className="h-7 w-7 shrink-0 px-0"
              data-testid="session-mcp-add"
              disabled={loading || (!hasSession && !hasWorkspace)}
              onClick={() => setDraft(createMcpDraft())}
              size="sm"
              title={t("panels.sessionMcp.addTitle")}
              variant="ghost"
            >
              <PlusIcon size={14} />
            </Button>
          ) : null}
          <Button
            aria-label={t("panels.sessionMcp.refresh")}
            className="h-7 w-7 shrink-0 px-0"
            disabled={loading || (!hasSession && !hasWorkspace)}
            onClick={() => void refresh()}
            size="sm"
            title={t("panels.sessionMcp.refresh")}
            variant="ghost"
          >
            <RefreshCwIcon className={cn(loading && "animate-spin")} size={14} />
          </Button>
        </div>
      </header>

      {scope ? <SessionMcpScopeCard scope={scope} /> : null}

      {hasWorkspace && hasSession ? (
        <p
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "app-text-11 leading-4 text-muted-foreground",
          )}
          data-testid="session-mcp-workspace-impact"
        >
          {t("panels.sessionMcp.workspaceImpact")}
        </p>
      ) : null}

      {!hasSession && !hasWorkspace ? (
        <p
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "app-text-11 leading-4 text-muted-foreground",
          )}
          data-testid="session-mcp-unavailable"
          role="status"
        >
          {t("panels.sessionMcp.unavailableNoWorkspace")}
        </p>
      ) : null}

      {draft ? (
        <div data-testid="session-mcp-add-form">
          <RuntimeMcpForm
            draft={draft}
            isSaving={Boolean(pendingName)}
            mode="create"
            onCancel={() => setDraft(null)}
            onChange={setDraft}
            onSubmit={() => {
              void submitAdd();
            }}
          />
        </div>
      ) : null}

      {notice ? (
        <p
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "app-text-11 leading-4 text-muted-foreground",
          )}
          data-testid="session-mcp-notice"
          role="status"
        >
          {notice}
        </p>
      ) : null}

      {loadFailed ? (
        <div
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "grid gap-2 text-xs text-accent-danger",
          )}
          data-testid="session-mcp-error"
          role="alert"
        >
          <div className="flex items-start gap-2">
            <AlertTriangleIcon className="mt-0.5 shrink-0" size={14} />
            <div className="min-w-0">
              <div className="font-medium">{t("panels.sessionMcp.errorTitle")}</div>
              <div className="mt-0.5 app-text-11 break-words text-muted-foreground">
                {error}
              </div>
            </div>
          </div>
          <div>
            <Button
              className="h-7 px-2 text-xs"
              onClick={() => void refresh()}
              size="sm"
              variant="ghost"
            >
              {t("panels.sessionMcp.retry")}
            </Button>
          </div>
        </div>
      ) : null}

      {!loadFailed && error ? (
        <p
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "app-text-11 break-words leading-4 text-accent-danger",
          )}
          data-testid="session-mcp-toggle-error"
          role="alert"
        >
          {error}
        </p>
      ) : null}

      {loading && entries.length === 0 ? (
        <p className="app-text-11 text-muted-foreground">
          {t("panels.sessionMcp.loading")}
        </p>
      ) : null}

      {!loading && !loadFailed && entries.length === 0 ? (
        <p
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "app-text-11 leading-4 text-muted-foreground",
          )}
          data-testid="session-mcp-empty"
        >
          {t("panels.sessionMcp.empty")}
        </p>
      ) : null}

      {entries.length > 0 ? (
        <SessionMcpRows
          disabledSet={disabledSet}
          entries={entries}
          manageEnabled={scope !== null && (hasWorkspace || hasSession)}
          onDelete={(entry) => {
            void handleDelete(entry);
          }}
          onTogglePersist={(entry) => {
            void handlePersistToggle(entry);
          }}
          onToggleSession={(entry) => {
            void handleToggle(entry);
          }}
          onViewTools={(entry) => {
            const name =
              entry.config?.name?.trim() || entry.status?.name?.trim();
            if (name) {
              setToolsFor(name);
            }
          }}
          pendingName={pendingName}
          hideGlobalFallbackActions={hasWorkspace}
          sessionControlsEnabled={hasSession}
          toolsEnabled={hasSession}
          tempEnabledSet={tempEnabledSet}
        />
      ) : null}

      {toolsFor && hasSession ? (
        <SessionMcpToolsDialog
          name={toolsFor}
          onClose={() => setToolsFor(null)}
          sessionId={sessionId}
        />
      ) : null}
    </section>
  );
}
