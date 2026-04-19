import { ShieldCheckIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import {
  editorControlClassName,
  editorSectionToggleClassName,
} from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { type RuntimeAuthConfigSummary } from "./runtime-config-domain-utils";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSubsectionCard } from "./settings-subsection-card";

type RuntimeAuthDomainEditorProps = {
  authConfig: RuntimeAuthConfigSummary;
  onChange: (next: RuntimeAuthConfigSummary) => void;
};

export function RuntimeAuthDomainEditor({
  authConfig,
  onChange,
}: RuntimeAuthDomainEditorProps) {
  function update(patch: Partial<RuntimeAuthConfigSummary>) {
    onChange({
      ...authConfig,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">Auth 配置</span>}
        icon={
          <SettingsPanelIcon>
            <ShieldCheckIcon size={16} />
          </SettingsPanelIcon>
        }
        description="用专用表单维护 JWT、管理端和 Access Key 鉴权字段。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{authConfig.adminAuthEnabled ? "管理端鉴权开" : "管理端鉴权关"}</Badge>
            <Badge>{authConfig.accessAuthEnabled ? "Access 鉴权开" : "Access 鉴权关"}</Badge>
            <Badge>
              {authConfig.accessAuthAllowAnonymous ? "允许匿名" : "禁止匿名"}
            </Badge>
          </SettingsBadgeList>
        }
      />

      <SettingsSubsectionCard
        title="JWT 与会话"
        description="这组字段通常会被启动环境变量覆盖，适合检查默认值和回退值。"
      >
        <div className="grid gap-3 xl:grid-cols-2">
          <ConfigFormField label="jwt_secret" description="建议生产环境通过 AUTH_JWT_SECRET 注入。">
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.jwtSecret}
              onChange={(event) => update({ jwtSecret: event.target.value })}
            />
          </ConfigFormField>
          <ConfigFormField
            label="access_key_secret"
            description="用于 Access Key 的密钥派生和恢复。"
          >
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.accessKeySecret}
              onChange={(event) => update({ accessKeySecret: event.target.value })}
            />
          </ConfigFormField>
          <ConfigFormField label="jwt_expire">
            <input
              className={editorControlClassName}
              value={authConfig.jwtExpire}
              onChange={(event) => update({ jwtExpire: event.target.value })}
              placeholder="24h"
            />
          </ConfigFormField>
          <ConfigFormField label="session_timeout">
            <input
              className={editorControlClassName}
              value={authConfig.sessionTimeout}
              onChange={(event) => update({ sessionTimeout: event.target.value })}
              placeholder="30m"
            />
          </ConfigFormField>
          <ConfigFormField label="max_api_create_times">
            <input
              className={editorControlClassName}
              value={authConfig.maxApiCreateTimes}
              onChange={(event) => update({ maxApiCreateTimes: event.target.value })}
              placeholder="100"
            />
          </ConfigFormField>
        </div>
      </SettingsSubsectionCard>

      <div className="grid gap-3 xl:grid-cols-2">
        <SettingsSubsectionCard
          title="管理端鉴权"
          description="`/admin/*` 的 JWT 与静态 token 配置。"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={authConfig.adminAuthEnabled}
                onChange={(event) =>
                  update({ adminAuthEnabled: event.target.checked })
                }
              />
              启用
            </label>
          }
        >

          <ConfigFormField label="admin_token" description="静态管理 token，通常只用于内部环境。">
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.adminToken}
              onChange={(event) => update({ adminToken: event.target.value })}
            />
          </ConfigFormField>
        </SettingsSubsectionCard>

        <SettingsSubsectionCard
          title="Access 鉴权"
          description="`/v1/*` 访问密钥鉴权开关和匿名访问策略。"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={authConfig.accessAuthEnabled}
                onChange={(event) =>
                  update({ accessAuthEnabled: event.target.checked })
                }
              />
              启用
            </label>
          }
        >

          <SettingsInlineToggleCard
            checked={authConfig.accessAuthAllowAnonymous}
            label="allow_anonymous"
            description="关闭后，未携带 Access Key 的请求会被直接拒绝。"
            labelClassName="items-start"
            onCheckedChange={(checked) =>
              update({ accessAuthAllowAnonymous: checked })
            }
          />
        </SettingsSubsectionCard>
      </div>
    </div>
  );
}
