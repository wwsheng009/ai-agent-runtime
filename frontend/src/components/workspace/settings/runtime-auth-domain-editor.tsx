import { ShieldCheckIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("runtimeConfig");

  function update(patch: Partial<RuntimeAuthConfigSummary>) {
    onChange({
      ...authConfig,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.auth.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <ShieldCheckIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.auth.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {authConfig.adminAuthEnabled
                ? t("editor.auth.badges.adminOn")
                : t("editor.auth.badges.adminOff")}
            </Badge>
            <Badge>
              {authConfig.accessAuthEnabled
                ? t("editor.auth.badges.accessOn")
                : t("editor.auth.badges.accessOff")}
            </Badge>
            <Badge>
              {authConfig.accessAuthAllowAnonymous
                ? t("editor.auth.badges.anonymousAllowed")
                : t("editor.auth.badges.anonymousDenied")}
            </Badge>
          </SettingsBadgeList>
        }
      />

      <SettingsSubsectionCard
        title={t("editor.auth.jwtSession.title")}
        description={t("editor.auth.jwtSession.description")}
      >
        <div className="grid gap-3 xl:grid-cols-2">
          <ConfigFormField
            label="jwt_secret"
            description={t("editor.auth.fields.jwtSecretDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.jwtSecret}
              onChange={(event) => update({ jwtSecret: event.target.value })}
            />
          </ConfigFormField>
          <ConfigFormField
            label="access_key_secret"
            description={t("editor.auth.fields.accessKeySecretDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.accessKeySecret}
              onChange={(event) =>
                update({ accessKeySecret: event.target.value })
              }
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
              onChange={(event) =>
                update({ sessionTimeout: event.target.value })
              }
              placeholder="30m"
            />
          </ConfigFormField>
          <ConfigFormField label="max_api_create_times">
            <input
              className={editorControlClassName}
              value={authConfig.maxApiCreateTimes}
              onChange={(event) =>
                update({ maxApiCreateTimes: event.target.value })
              }
              placeholder="100"
            />
          </ConfigFormField>
        </div>
      </SettingsSubsectionCard>

      <div className="grid gap-3 xl:grid-cols-2">
        <SettingsSubsectionCard
          title={t("editor.auth.admin.title")}
          description={t("editor.auth.admin.description")}
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
              {t("editor.auth.enable")}
            </label>
          }
        >
          <ConfigFormField
            label="admin_token"
            description={t("editor.auth.admin.tokenDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={authConfig.adminToken}
              onChange={(event) => update({ adminToken: event.target.value })}
            />
          </ConfigFormField>
        </SettingsSubsectionCard>

        <SettingsSubsectionCard
          title={t("editor.auth.access.title")}
          description={t("editor.auth.access.description")}
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
              {t("editor.auth.enable")}
            </label>
          }
        >
          <SettingsInlineToggleCard
            checked={authConfig.accessAuthAllowAnonymous}
            label="allow_anonymous"
            description={t("editor.auth.access.allowAnonymousDescription")}
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
