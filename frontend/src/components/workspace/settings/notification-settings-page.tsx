import {
  BellDotIcon,
  BellOffIcon,
  MonitorSmartphoneIcon,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useAppSettings } from "@/core/settings";

import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

function readNotificationPermission() {
  if (typeof Notification === "undefined") {
    return "unsupported" as const;
  }

  return Notification.permission;
}

export function NotificationSettingsPage() {
  const { t } = useTranslation("settings");
  const { settings, updateSection } = useAppSettings();
  const [permission, setPermission] = useState(readNotificationPermission);

  async function requestPermission() {
    if (typeof Notification === "undefined") {
      setPermission("unsupported");
      return;
    }

    const result = await Notification.requestPermission();
    setPermission(result);
  }

  const desktopSupported = permission !== "unsupported";
  const desktopReady = permission === "granted";

  return (
    <div className="space-y-6">
      <SettingsSection title={t("notifications.title")}>
        <SettingsToggleCard
          checked={settings.notification.enabled}
          onChange={(checked) =>
            updateSection("notification", {
              enabled: checked,
            })
          }
          title={t("notifications.masterSwitch")}
          icon={
            settings.notification.enabled ? (
              <BellDotIcon aria-hidden="true" size={16} />
            ) : (
              <BellOffIcon aria-hidden="true" size={16} />
            )
          }
        />
        <SettingsToggleCard
          checked={settings.notification.desktop}
          disabled={!settings.notification.enabled || !desktopSupported}
          onChange={(checked) =>
            updateSection("notification", {
              desktop: checked,
            })
          }
          title={t("notifications.desktop")}
          icon={<MonitorSmartphoneIcon aria-hidden="true" size={16} />}
        />
      </SettingsSection>

      <SettingsSection title={t("notifications.permission")}>
        <SettingsPanelCard
          title={t("notifications.browserPermission")}
          description={
            permission === "granted"
              ? t("notifications.permissionStates.granted")
              : permission === "denied"
                ? t("notifications.permissionStates.denied")
                : permission === "default"
                  ? t("notifications.permissionStates.default")
                  : t("notifications.permissionStates.unsupported")
          }
          headerClassName="flex-col gap-3 lg:flex-row lg:items-start lg:justify-between"
          asideClassName="w-full lg:w-auto"
          headerAside={
            <div className="flex flex-wrap gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void requestPermission()}
                disabled={
                  !desktopSupported ||
                  desktopReady ||
                  !settings.notification.enabled
                }
              >
                {t("notifications.requestPermission")}
              </Button>
            </div>
          }
        />

      </SettingsSection>
    </div>
  );
}
