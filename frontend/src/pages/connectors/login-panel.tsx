// 扫码登录面板：start_login → 展示二维码/链接 → 自动轮询 login_status（~2s，
// 止于 confirmed/expired 或 expires_at 到期，可手动刷新）→ confirmed 且
// credential_ready 时把 settings/secrets 合并 PATCH 回连接器并回调父页刷新。
//
// 状态机：starting → wait | scanned（轮询中）→ confirmed（回填）| expired（停止）。
// 轮询失败即停（避免打爆上游），可手动刷新恢复；Escape 关闭并中止轮询；
// 回填保存中通过 onBusyChange 通知外层拦截关闭。
//
// 该面板只负责「面板内容」（可选标题 + 主体 + 底栏），外层弹窗（action-dialog）
// 与「新增连接器」对话框内嵌（connector-dialog）共用同一份逻辑。

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toString as qrToString } from "qrcode";

import { runConnectorAction, updateConnector } from "@/api/runtime/connectors";
import { Button } from "@/components/ui/button";
import { readAdminToken } from "@/lib/admin-token";
import type {
  ConnectorActionPayload,
  ConnectorActionSpec,
} from "@/types/runtime";

import { isDirectImageSource } from "./action-payload";

/** 轮询间隔；后端 login_status 自身是长轮询，2s 只是兜底节流。 */
const POLL_INTERVAL_MS = 2000;
const LOGIN_STATUS_ACTION = "login_status";

interface ConnectorLoginPanelProps {
  connectorId: string;
  connectorName: string;
  action: ConnectorActionSpec;
  /** 凭据回填成功后回调（父页刷新列表）。 */
  onRefilled?: () => void;
  onClose: () => void;
  /** 回填保存中=true：外层遮罩/Escape 据此拦截关闭。 */
  onBusyChange?: (busy: boolean) => void;
  /** 是否渲染标题头（独立对话框=true；内嵌进其它对话框=false）。 */
  showHeader?: boolean;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function expiresAtMs(payload: ConnectorActionPayload | null): number | null {
  const raw = payload?.expires_at?.trim();
  if (!raw) {
    return null;
  }
  const parsed = Date.parse(raw);
  return Number.isFinite(parsed) ? parsed : null;
}

export function ConnectorLoginPanel({
  connectorId,
  connectorName,
  action,
  onRefilled,
  onClose,
  onBusyChange,
  showHeader = false,
}: ConnectorLoginPanelProps) {
  const { t } = useTranslation("connectors");
  const adminToken = readAdminToken();
  const actionKey = action.key;

  const [payload, setPayload] = useState<ConnectorActionPayload | null>(null);
  const [starting, setStarting] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [expired, setExpired] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refilling, setRefilling] = useState(false);
  const [refilled, setRefilled] = useState(false);
  const [refillError, setRefillError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  /** 本地编码出的二维码图（data:image/svg+xml）；非图片内容才有值。 */
  const [qrImage, setQrImage] = useState("");
  /** 已回填过的 login_id（轮询重复渲染不得重复 PATCH）。 */
  const refilledLoginId = useRef<string | null>(null);

  const loginId = payload?.login_id ?? "";
  const status = expired ? "expired" : payload?.status ?? "";
  const expiresAt = expiresAtMs(payload);

  useEffect(() => {
    onBusyChange?.(refilling);
  }, [refilling, onBusyChange]);

  // start_login：挂载（或切换目标连接器/动作）时新建一次登录会话。
  useEffect(() => {
    if (!connectorId || !actionKey) {
      return;
    }
    let cancelled = false;
    refilledLoginId.current = null;
    setPayload(null);
    setStarting(true);
    setExpired(false);
    setError(null);
    setRefilled(false);
    setRefillError(null);
    setCopied(false);
    setCopyFailed(false);
    void (async () => {
      try {
        const result = await runConnectorAction(
          connectorId,
          actionKey,
          undefined,
          { adminToken },
        );
        if (!cancelled) {
          setPayload(result);
        }
      } catch (caught) {
        if (!cancelled) {
          setError(errorText(caught));
        }
      } finally {
        if (!cancelled) {
          setStarting(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [connectorId, actionKey, adminToken]);

  // 轮询：链式 setTimeout（避免长轮询未返回时请求堆叠）。
  const pollingActive =
    connectorId !== "" &&
    loginId !== "" &&
    (status === "wait" || status === "scanned");
  useEffect(() => {
    if (!pollingActive) {
      return;
    }
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      if (cancelled) {
        return;
      }
      if (expiresAt !== null && expiresAt <= Date.now()) {
        setExpired(true); // expires_at 到期：停止轮询，等用户手动刷新
        return;
      }
      try {
        const next = await runConnectorAction(
          connectorId,
          LOGIN_STATUS_ACTION,
          { login_id: loginId },
          { adminToken },
        );
        if (cancelled) {
          return;
        }
        setPayload(next);
        setExpired(false);
      } catch (caught) {
        if (!cancelled) {
          setError(errorText(caught));
        }
        return;
      }
      timer = setTimeout(() => void tick(), POLL_INTERVAL_MS);
    };
    timer = setTimeout(() => void tick(), POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      if (timer) {
        clearTimeout(timer);
      }
    };
  }, [pollingActive, connectorId, loginId, adminToken, expiresAt]);

  // confirmed + credential_ready：把 settings/secrets 合并 PATCH 回连接器（同一 login_id 只做一次）。
  useEffect(() => {
    if (!connectorId || !payload) {
      return undefined;
    }
    if (payload.status !== "confirmed" || payload.credential_ready !== true) {
      return undefined;
    }
    if (!payload.settings && !payload.secrets) {
      return undefined;
    }
    const confirmedLoginId = payload.login_id || "confirmed";
    if (refilledLoginId.current === confirmedLoginId) {
      return undefined;
    }
    refilledLoginId.current = confirmedLoginId;
    let cancelled = false;
    setRefilling(true);
    setRefillError(null);
    void (async () => {
      try {
        await updateConnector(
          connectorId,
          { settings: payload.settings, secrets: payload.secrets },
          { adminToken },
        );
        if (!cancelled) {
          setRefilled(true);
          onRefilled?.();
        }
      } catch (caught) {
        if (!cancelled) {
          refilledLoginId.current = null; // 允许手动刷新后重试
          setRefillError(errorText(caught));
        }
      } finally {
        if (!cancelled) {
          setRefilling(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [connectorId, payload, adminToken, onRefilled]);

  // Escape 关闭并中止轮询；回填保存中拦截（与遮罩/按钮一致）。
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || refilling) {
        return;
      }
      onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [refilling, onClose]);

  const qrContent = payload?.qr_code_content?.trim() ?? "";
  const directImage = isDirectImageSource(qrContent);
  // 非图片内容（微信返回的是 liteapp 链接）在本地编码成二维码 SVG，避免坏图。
  useEffect(() => {
    if (!qrContent || directImage) {
      setQrImage("");
      return;
    }
    let cancelled = false;
    void qrToString(qrContent, { type: "svg", margin: 1, width: 320 })
      .then((svg) => {
        if (!cancelled) {
          setQrImage(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setQrImage("");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [qrContent, directImage]);
  const qrImageSrc = directImage ? qrContent : qrImage;
  const statusText = (() => {
    switch (status) {
      case "wait":
        return t("actionDialog.statusWait");
      case "scanned":
        return t("actionDialog.statusScanned");
      case "confirmed":
        return t("actionDialog.statusConfirmed");
      case "expired":
        return t("actionDialog.statusExpired");
      case "":
        return t("actionDialog.starting");
      default:
        return t("actionDialog.statusUnknown", { status });
    }
  })();

  const handleRefresh = async () => {
    setRefreshing(true);
    setError(null);
    try {
      const next = loginId
        ? await runConnectorAction(
            connectorId,
            LOGIN_STATUS_ACTION,
            { login_id: loginId },
            { adminToken },
          )
        : await runConnectorAction(connectorId, actionKey, undefined, {
            adminToken,
          });
      setPayload(next);
      setExpired(false);
    } catch (caught) {
      setError(errorText(caught));
    } finally {
      setRefreshing(false);
    }
  };

  const handleCopy = async () => {
    if (!qrContent) {
      return;
    }
    try {
      await navigator.clipboard.writeText(qrContent);
      setCopied(true);
      setCopyFailed(false);
    } catch {
      setCopyFailed(true);
    }
  };

  return (
    <>
      {showHeader ? (
        <header className="border-b border-border px-4 py-3">
          <h2 className="text-sm font-semibold text-foreground">
            {t("actionDialog.title", { name: connectorName })}
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{action.label}</p>
        </header>
      ) : null}

      <div className="flex flex-col gap-3 px-4 py-3">
        {starting ? (
          <p className="text-sm text-muted-foreground">
            {t("actionDialog.starting")}
          </p>
        ) : null}
        {error ? (
          <p className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
            {t("actionDialog.startFailed", { message: error })}
          </p>
        ) : null}

        {qrContent ? (
          qrImageSrc ? (
            <img
              src={qrImageSrc}
              alt={t("actionDialog.qrAlt")}
              className="mx-auto h-48 w-48 rounded-card border border-border bg-surface-soft object-contain"
            />
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("actionDialog.qrRendering")}
            </p>
          )
        ) : null}

        {qrContent ? (
          <div className="flex flex-col gap-1.5">
            <span className="text-xs text-muted-foreground">
              {t("actionDialog.qrContentLabel")}
            </span>
            <code className="break-all rounded-card border border-border bg-surface-soft px-3 py-2 text-xs text-foreground">
              {qrContent}
            </code>
          </div>
        ) : null}

        {payload ? (
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span>{statusText}</span>
            {payload.expires_at ? (
              <span>
                · {t("actionDialog.expiresAt", { time: payload.expires_at })}
              </span>
            ) : null}
            {payload.refresh_count ? (
              <span>
                ·{" "}
                {t("actionDialog.refreshCount", {
                  times: String(payload.refresh_count),
                })}
              </span>
            ) : null}
          </div>
        ) : null}

        {refillError ? (
          <p className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
            {t("actionDialog.refillFailed", { message: refillError })}
          </p>
        ) : null}
        {refilled ? (
          <p className="rounded-card border border-accent-primary-border bg-accent-primary-soft px-3 py-2 text-sm text-muted-foreground">
            {t("actionDialog.refilled")}
          </p>
        ) : refilling ? (
          <p className="text-sm text-muted-foreground">
            {t("actionDialog.credentialReady")}
          </p>
        ) : null}
        {copyFailed ? (
          <p className="text-sm text-muted-foreground">
            {t("actionDialog.copyFailed")}
          </p>
        ) : copied ? (
          <p className="text-sm text-muted-foreground">
            {t("actionDialog.copied")}
          </p>
        ) : null}
      </div>

      <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-4 py-3">
        {qrContent ? (
          <Button variant="ghost" size="sm" onClick={() => void handleCopy()}>
            {t("actionDialog.copyLink")}
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="sm"
          disabled={starting || refreshing || refilling}
          onClick={() => void handleRefresh()}
        >
          {refreshing ? t("actionDialog.refreshing") : t("actionDialog.refresh")}
        </Button>
        <Button variant="ghost" size="sm" disabled={refilling} onClick={onClose}>
          {t("actionDialog.close")}
        </Button>
      </footer>
    </>
  );
}
