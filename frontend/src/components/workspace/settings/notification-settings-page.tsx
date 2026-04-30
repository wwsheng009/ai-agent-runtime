import {
  BellDotIcon,
  BellOffIcon,
  MonitorSmartphoneIcon,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useAppSettings } from "@/core/settings";

import { SettingsInfoCard } from "./settings-info-card";
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
      <SettingsSection
        title={t("notifications.title")}
        description={t("notifications.description")}
      >
        <SettingsToggleCard
          checked={settings.notification.enabled}
          onChange={(checked) =>
            updateSection("notification", {
              enabled: checked,
            })
          }
          title={t("notifications.desktop")}
          description={t("notifications.desktopDescription")}
          icon={
            settings.notification.enabled ? (
              <BellDotIcon size={16} />
            ) : (
              <BellOffIcon size={16} />
            )
          }
        />
      </SettingsSection>

      <SettingsSection
        title={t("notifications.permission")}
        description={t("notifications.permissionDescription")}
      >
        <SettingsPanelCard
          title={t("notifications.permission")}
          icon={
            <MonitorSmartphoneIcon
              size={16}
              className="text-[var(--accent-secondary)]"
            />
          }
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
                variant="secondary"
                size="sm"
                onClick={() =>
                  updateSection("notification", {
                    desktop: !settings.notification.desktop,
                  })
                }
                disabled={!desktopSupported || !settings.notification.enabled}
              >
                {settings.notification.desktop
                  ? t("notifications.disableDesktop")
                  : t("notifications.enableDesktop")}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void requestPermission()}
                disabled={!desktopSupported || desktopReady}
              >
                {t("notifications.requestPermission")}
              </Button>
            </div>
          }
        />

        <div className="grid gap-3 md:grid-cols-2">
          <SettingsInfoCard
            title={t("notifications.currentConfig")}
            description={
              <>
                {t("notifications.currentConfigMasterSwitch")}:{" "}
                <span className="text-[var(--foreground)]">
                  {settings.notification.enabled
                    ? t("notifications.enabled")
                    : t("notifications.disabled")}
                </span>
                <br />
                {t("notifications.currentConfigDesktopSwitch")}:{" "}
                <span className="text-[var(--foreground)]">
                  {settings.notification.desktop
                    ? t("notifications.enabled")
                    : t("notifications.disabled")}
                </span>
              </>
            }
          />
          <SettingsInfoCard
            title={t("notifications.effectiveCondition")}
            description={t("notifications.effectiveConditionDescription")}
          />
        </div>
      </SettingsSection>
    </div>
  );
}
