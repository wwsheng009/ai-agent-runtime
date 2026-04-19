import {
  BellDotIcon,
  BellOffIcon,
  MonitorSmartphoneIcon,
} from "lucide-react";
import { useState } from "react";

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
        title="工作区通知"
        description="仅在当前标签页不可见时，用于提醒一轮响应完成或运行时出错。"
      >
        <SettingsToggleCard
          checked={settings.notification.enabled}
          onChange={(checked) =>
            updateSection("notification", {
              enabled: checked,
            })
          }
          title="启用通知能力"
          description="关闭后，即便浏览器权限已授权，也不会弹出桌面提醒。"
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
        title="桌面提醒"
        description="需要浏览器授权。若当前页面可见，则不会打断你。"
      >
        <SettingsPanelCard
          title="权限状态"
          icon={
            <MonitorSmartphoneIcon
              size={16}
              className="text-[var(--accent-secondary)]"
            />
          }
          description={
            permission === "granted"
              ? "已授权，可以在后台收到完成或错误通知。"
              : permission === "denied"
                ? "已被浏览器阻止，需要在站点权限中手动重新允许。"
                : permission === "default"
                  ? "尚未授权，点击右侧按钮向浏览器申请通知权限。"
                  : "当前环境不支持浏览器桌面通知。"
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
                {settings.notification.desktop ? "关闭桌面提醒" : "开启桌面提醒"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void requestPermission()}
                disabled={!desktopSupported || desktopReady}
              >
                请求权限
              </Button>
            </div>
          }
        />

        <div className="grid gap-3 md:grid-cols-2">
          <SettingsInfoCard
            title="当前配置"
            description={
              <>
                通知总开关:{" "}
                <span className="text-[var(--foreground)]">
                  {settings.notification.enabled ? "开启" : "关闭"}
                </span>
                <br />
                桌面提醒:{" "}
                <span className="text-[var(--foreground)]">
                  {settings.notification.desktop ? "开启" : "关闭"}
                </span>
              </>
            }
          />
          <SettingsInfoCard
            title="实际生效条件"
            description="只有在总开关开启、桌面提醒开启、权限为 granted 且页面处于后台时，通知才会真正弹出。"
          />
        </div>
      </SettingsSection>
    </div>
  );
}
