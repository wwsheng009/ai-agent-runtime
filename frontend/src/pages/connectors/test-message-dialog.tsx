// 测试消息对话框：向指定 IM 会话发一条消息，验证凭据与通道链路。
// 端点：POST /api/connectors/{id}/test（restapi.connectors.go）。

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { sendConnectorTestMessage } from "@/api/runtime/connectors";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { readAdminToken } from "@/lib/admin-token";

import type { ConnectorView } from "@/types/runtime";

const inputClassName =
  "h-9 w-full rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary";

interface TestMessageDialogProps {
  open: boolean;
  connector: ConnectorView | null;
  onClose: () => void;
}

export function TestMessageDialog({ open, connector, onClose }: TestMessageDialogProps) {
  const { t } = useTranslation("connectors");
  const [chat, setChat] = useState("");
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    setChat("");
    setText("");
    setSending(false);
    setSent(false);
    setError(null);
  }, [open, connector]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      if (sending) {
        return; // 发送中与遮罩/取消按钮一致：拦截关闭
      }
      onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, sending, onClose]);

  if (!open || !connector) {
    return null;
  }

  const handleSend = async () => {
    setSending(true);
    setError(null);
    try {
      await sendConnectorTestMessage(
        connector.id,
        { chat, text },
        { adminToken: readAdminToken() },
      );
      setSent(true);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSending(false);
    }
  };

  return (
    <DialogOverlay onDismiss={sending ? () => undefined : onClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={t("test.title")}
        className="max-w-md"
      >
        <header className="border-b border-border px-4 py-3">
          <h2 className="text-sm font-semibold text-foreground">
            {t("test.title")}
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {connector.name} · {connector.provider}
          </p>
        </header>

        <div className="flex flex-col gap-3 px-4 py-3">
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            {t("test.chat")}
            <input
              type="text"
              aria-label={t("test.chat")}
              className={inputClassName}
              placeholder={t("test.chatPlaceholder")}
              value={chat}
              onChange={(event) => setChat(event.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            {t("test.text")}
            <input
              type="text"
              aria-label={t("test.text")}
              className={inputClassName}
              placeholder={t("test.textPlaceholder")}
              value={text}
              onChange={(event) => setText(event.target.value)}
            />
          </label>

          {sent ? (
            <p className="rounded-card border border-accent-primary-border bg-accent-primary-soft px-3 py-2 text-sm text-muted-foreground">
              {t("test.success")}
            </p>
          ) : null}
          {error ? (
            <p className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("test.failed", { message: error })}
            </p>
          ) : null}
        </div>

        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={sending}>
            {t("test.cancel")}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={() => void handleSend()}
            disabled={sending || chat.trim().length === 0}
          >
            {sending ? t("test.sending") : t("test.send")}
          </Button>
        </footer>
      </DialogPanel>
    </DialogOverlay>
  );
}
