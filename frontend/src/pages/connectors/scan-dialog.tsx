// 扫码接入对话框：为「扫码即得凭据」的通道（当前=微信）提供零填表入口。
//
// 与 connector-dialog 的分工：本对话框是「简单路径」——自动生成连接器 ID、
// 只让用户确认扫描基址（微信=`base_url` iLink 基址），创建后立即拉起扫码登录；
// 完整字段/编辑仍在 connector-dialog，二维码展示/轮询/回填复用 action-dialog。
//
// 通道差异全部来自 Schema：可扫码通道 = 声明了 kind=qr 动作的通道；扫描基址 =
// 该通道的 `base_url` 字段（标签/帮助文案均取自 Schema，前端零硬编码）。

import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { createConnector } from "@/api/runtime/connectors";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { readAdminToken } from "@/lib/admin-token";
import type { ConnectorSchema, ConnectorView } from "@/types/runtime";

import {
  generateConnectorId,
  scanBaseField,
  scanProviders,
} from "./shared";

const inputClassName =
  "h-9 w-full rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary disabled:opacity-60";

interface ConnectorScanDialogProps {
  open: boolean;
  schema: ConnectorSchema | null;
  /** 已有连接器：用于预填同通道基址，减少输入。 */
  existingConnectors: ConnectorView[];
  /** 创建成功：父页刷新列表并拉起扫码登录弹窗。 */
  onCreated: (connector: ConnectorView) => void;
  onClose: () => void;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** 预填同通道已配置的基址（base_url 优先，其次 api_base）；无则空串。 */
function prefillBaseUrl(connectors: ConnectorView[], provider: string): string {
  const match = connectors.find(
    (item) =>
      item.provider === provider &&
      (item.settings?.base_url || item.settings?.api_base),
  );
  return match?.settings?.base_url || match?.settings?.api_base || "";
}

export function ConnectorScanDialog({
  open,
  schema,
  existingConnectors,
  onCreated,
  onClose,
}: ConnectorScanDialogProps) {
  const { t } = useTranslation("connectors");
  const adminToken = readAdminToken();
  const providers = useMemo(() => scanProviders(schema), [schema]);

  const [provider, setProvider] = useState("");
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const first = providers[0]?.provider ?? "";
    setProvider(first);
    setName("");
    setBaseUrl(prefillBaseUrl(existingConnectors, first));
    setCreating(false);
    setError(null);
  }, [open, providers, existingConnectors]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || creating) {
        return;
      }
      onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, creating, onClose]);

  if (!open) {
    return null;
  }

  const providerSchema =
    providers.find((item) => item.provider === provider) ?? providers[0] ?? null;
  const baseField = scanBaseField(providerSchema);

  const switchTo = (next: string) => {
    setProvider(next);
    setName("");
    setBaseUrl(prefillBaseUrl(existingConnectors, next));
    setError(null);
  };

  const handleSubmit = async () => {
    if (!providerSchema) {
      return;
    }
    const base = baseUrl.trim();
    if (baseField && !base) {
      setError(t("scan.baseRequired", { label: baseField.label }));
      return;
    }
    setCreating(true);
    setError(null);
    try {
      const view = await createConnector(
        {
          // ID 自动生成：用户无需关心内部标识。
          id: generateConnectorId(providerSchema.provider),
          provider: providerSchema.provider,
          name: name.trim() || providerSchema.label,
          enabled: true,
          mode: providerSchema.modes?.[0] ?? "",
          settings: baseField ? { [baseField.key]: base } : {},
        },
        { adminToken },
      );
      onCreated(view);
    } catch (caught) {
      setError(errorText(caught));
      setCreating(false);
    }
  };

  return (
    <DialogOverlay onDismiss={creating ? () => undefined : onClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={t("scan.title")}
        className="max-w-md"
      >
        <header className="border-b border-border px-4 py-3">
          <h2 className="text-sm font-semibold text-foreground">{t("scan.title")}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{t("scan.hint")}</p>
        </header>

        <div className="flex flex-col gap-3 px-4 py-3">
          {error ? (
            <p className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("scan.failed", { message: error })}
            </p>
          ) : null}

          {providers.length > 1 ? (
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              {t("scan.provider")}
              <select
                aria-label={t("scan.provider")}
                className={inputClassName}
                value={provider}
                onChange={(event) => switchTo(event.target.value)}
              >
                {providers.map((item) => (
                  <option key={item.provider} value={item.provider}>
                    {item.label}
                  </option>
                ))}
              </select>
            </label>
          ) : providerSchema ? (
            <p className="text-xs text-muted-foreground">
              {t("scan.providerValue", { label: providerSchema.label })}
            </p>
          ) : null}

          {baseField ? (
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              <span>{baseField.label}</span>
              <input
                type="url"
                aria-label={baseField.label}
                className={inputClassName}
                placeholder={baseField.placeholder}
                value={baseUrl}
                onChange={(event) => {
                  setBaseUrl(event.target.value);
                  setError(null);
                }}
              />
              {baseField.help ? (
                <span className="text-xs text-muted-foreground">{baseField.help}</span>
              ) : null}
            </label>
          ) : null}

          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            {t("scan.name")}
            <input
              type="text"
              aria-label={t("scan.name")}
              className={inputClassName}
              placeholder={t("scan.namePlaceholder")}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </label>

          <p className="text-xs text-muted-foreground">{t("scan.idNote")}</p>
        </div>

        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={creating}>
            {t("scan.cancel")}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={() => void handleSubmit()}
            disabled={creating || providerSchema === null}
          >
            {creating ? t("scan.creating") : t("scan.start")}
          </Button>
        </footer>
      </DialogPanel>
    </DialogOverlay>
  );
}
