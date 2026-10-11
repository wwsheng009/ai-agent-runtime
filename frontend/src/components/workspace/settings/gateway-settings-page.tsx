import { PlugZapIcon, RefreshCwIcon, ServerIcon, UnplugIcon } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  connectGateway,
  disconnectGateway,
  getGatewayConfig,
  reconnectGateway,
  updateGatewayConfig,
  type RuntimeGatewayConfig,
  type RuntimeGatewayStatus,
} from "@/api/runtime/gateway";
import { Button } from "@/components/ui/button";

import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

const transitioningStates = new Set<string>(["connecting", "disconnecting"]);

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Gateway settings page (docs/gateway/12-connection-control.md §6.2): shows the
 * connection status and exposes manual connect / disconnect / reconnect plus the
 * endpoint configuration. The device token is never echoed back by the API, so
 * the token field always starts empty (leave blank to keep the stored value).
 */
export function GatewaySettingsPage() {
  const { t } = useTranslation("settings");
  const [status, setStatus] = useState<RuntimeGatewayStatus | null>(null);
  const [config, setConfig] = useState<RuntimeGatewayConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [token, setToken] = useState("");
  const [autoConnect, setAutoConnect] = useState(true);

  const load = useCallback(async () => {
    try {
      const snapshot = await getGatewayConfig();
      setConfig(snapshot.config);
      setStatus(snapshot.status);
      setUrl(snapshot.config.url ?? "");
      setName(snapshot.config.name ?? "");
      setAutoConnect(snapshot.config.auto_connect);
      setToken("");
      setError(null);
    } catch (err) {
      setError(describeError(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const runAction = useCallback(async (action: () => Promise<RuntimeGatewayStatus>) => {
    setBusy(true);
    try {
      setStatus(await action());
      setError(null);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  }, []);

  const save = useCallback(async () => {
    setBusy(true);
    try {
      const snapshot = await updateGatewayConfig({
        url: url.trim(),
        name: name.trim(),
        auto_connect: autoConnect,
        ...(token.trim() ? { token: token.trim() } : {}),
      });
      setConfig(snapshot.config);
      setStatus(snapshot.status);
      setToken("");
      setError(null);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  }, [autoConnect, name, token, url]);

  const disabled = busy || (status ? transitioningStates.has(status.state) : false);
  const notConfigured = status === null;

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("gateway.title")}
        description={t("gateway.description")}
      >
        {notConfigured ? (
          <p className="text-sm text-muted-foreground">
            {t("gateway.notConfigured")}
          </p>
        ) : (
          <dl className="grid grid-cols-1 gap-2 text-sm sm:grid-cols-2">
            <StatusRow label={t("gateway.state")} value={status?.state ?? "—"} />
            <StatusRow label={t("gateway.nodeId")} value={status?.node_id || "—"} />
            <StatusRow
              label={t("gateway.consoleUrl")}
              value={status?.console_url || "—"}
            />
            <StatusRow
              label={t("gateway.sessions")}
              value={String(status?.sessions ?? 0)}
            />
            <StatusRow
              label={t("gateway.reconnects")}
              value={String(status?.reconnect_count ?? 0)}
            />
            <StatusRow label={t("gateway.mode")} value={status?.mode ?? "—"} />
          </dl>
        )}

        {error ? (
          <p className="mt-3 text-sm text-destructive">{error}</p>
        ) : null}

        <div className="mt-4 flex flex-wrap gap-2">
          <Button type="button" disabled={disabled} onClick={() => void runAction(connectGateway)}>
            <PlugZapIcon size={16} />
            {t("gateway.connect")}
          </Button>
          <Button type="button" disabled={disabled} onClick={() => void runAction(disconnectGateway)}>
            <UnplugIcon size={16} />
            {t("gateway.disconnect")}
          </Button>
          <Button type="button" disabled={disabled} onClick={() => void runAction(reconnectGateway)}>
            <RefreshCwIcon size={16} />
            {t("gateway.reconnect")}
          </Button>
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("gateway.configTitle")}
        description={t("gateway.configDescription")}
      >
        <div className="space-y-3">
          <Field label={t("gateway.url")}>
            <input
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              placeholder={t("gateway.urlPlaceholder")}
            />
          </Field>
          <Field label={t("gateway.name")}>
            <input
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t("gateway.namePlaceholder")}
            />
          </Field>
          <Field label={t("gateway.token")}>
            <input
              type="password"
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              placeholder={
                config?.token_hint
                  ? t("gateway.tokenHint", { hint: config.token_hint })
                  : t("gateway.tokenPlaceholder")
              }
            />
          </Field>

          <SettingsToggleCard
            checked={autoConnect}
            onChange={setAutoConnect}
            title={t("gateway.autoConnect")}
            description={t("gateway.autoConnectDescription")}
            icon={<ServerIcon size={16} />}
          />

          <Button type="button" disabled={busy} onClick={() => void save()}>
            {t("gateway.save")}
          </Button>
        </div>
      </SettingsSection>
    </div>
  );
}

function StatusRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="truncate font-mono text-xs">{value}</dd>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-sm text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}
