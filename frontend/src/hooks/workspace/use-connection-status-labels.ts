/**
 * P1-8：工作台「连接状态」文案统一出口。
 * 顶栏、消息流尾与直连 chat 恢复面共用同一份标签，避免多处各写一套。
 */

import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { type ConnectionStatusLabels } from "@/lib/connection-status";

export function useConnectionStatusLabels() {
  const { t } = useTranslation("workspace");
  const labels = useMemo<ConnectionStatusLabels>(
    () => ({
      connecting: t("topbar.connection.connecting"),
      idle: t("topbar.connection.idle"),
      offline: t("topbar.connection.offline"),
      online: t("topbar.connection.online"),
      reconnecting: t("topbar.connection.reconnecting"),
    }),
    [t],
  );

  return { labels, retryLabel: t("topbar.connection.retry") };
}
