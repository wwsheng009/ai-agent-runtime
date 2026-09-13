import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";
import { SettingsNoticeCard } from "../settings-notice-card";

import { type AccountAction } from "./draft-utils";

type ProviderAccountSectionProps = {
  accountBusy: AccountAction;
  accountError: string | null;
  accountNotice: string | null;
  accountSummaryLine: string;
  draft: ProviderDraftInput;
  editingProviderName: string | null;
  onDetectSiteType: () => void;
  onFetchAccount: () => void;
  onRefreshProviderAccount: (
    providerName: string,
    options?: { fromDialog?: boolean },
  ) => void;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

export function ProviderAccountSection({
  accountBusy,
  accountError,
  accountNotice,
  accountSummaryLine,
  draft,
  editingProviderName,
  onDetectSiteType,
  onFetchAccount,
  onRefreshProviderAccount,
  setDraft,
}: ProviderAccountSectionProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <>
          <div className="rounded-[0.8rem] border border-border bg-surface-softer p-3">
            <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold text-foreground">
                  {t("editor.providers.account.title")}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {t("editor.providers.account.description")}
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                {draft.siteType ? (
                  <Badge>
                    {draft.siteType}
                    {draft.siteTypeConfidence
                      ? ` · ${draft.siteTypeConfidence}`
                      : ""}
                  </Badge>
                ) : (
                  <Badge>{t("editor.providers.account.undetected")}</Badge>
                )}
                {draft.accountAuthRef ? (
                  <Badge>{t("editor.providers.account.authRefBadge")}</Badge>
                ) : null}
              </div>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="site_type"
                description={t("editor.providers.account.siteTypeDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.siteType}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteType: event.target.value,
                    }))
                  }
                  placeholder="sub2api / newapi / unknown"
                />
              </ConfigFormField>
              <ConfigFormField
                label="account_auth_ref"
                description={t("editor.providers.account.accountAuthRefDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.accountAuthRef}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      accountAuthRef: event.target.value,
                    }))
                  }
                  placeholder="providers/<name>/account"
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="site_type_confidence">
                <input
                  className={editorControlClassName}
                  value={draft.siteTypeConfidence}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteTypeConfidence: event.target.value,
                    }))
                  }
                  placeholder="high / medium / low"
                />
              </ConfigFormField>
              <ConfigFormField label="site_type_detected_at">
                <input
                  className={editorControlClassName}
                  value={draft.siteTypeDetectedAt}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteTypeDetectedAt: event.target.value,
                    }))
                  }
                  placeholder="ISO timestamp"
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="system_access_token"
                description={t("editor.providers.account.systemAccessTokenDescription")}
              >
                <input
                  className={editorControlClassName}
                  type="password"
                  autoComplete="off"
                  value={draft.systemAccessToken}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      systemAccessToken: event.target.value,
                    }))
                  }
                  placeholder="NewAPI system access token"
                />
              </ConfigFormField>
              <ConfigFormField
                label="subject_user_id"
                description={t("editor.providers.account.subjectUserIdDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.subjectUserId}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      subjectUserId: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.account.subjectUserIdPlaceholder")}
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null}
                onClick={() => void onDetectSiteType()}
              >
                {accountBusy === "detect"
                  ? t("editor.providers.account.detecting")
                  : t("editor.providers.account.detect")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null}
                onClick={() => void onFetchAccount()}
              >
                {accountBusy === "fetch"
                  ? t("editor.providers.account.fetching")
                  : t("editor.providers.account.fetch")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null || !editingProviderName}
                onClick={() =>
                  void onRefreshProviderAccount(
                    editingProviderName || draft.name,
                    { fromDialog: true },
                  )
                }
              >
                {accountBusy === "refresh"
                  ? t("editor.providers.account.refreshing")
                  : t("editor.providers.account.refresh")}
              </Button>
            </div>

            {accountSummaryLine ? (
              <SettingsNoticeCard tone="muted" className="mt-3">
                {accountSummaryLine}
              </SettingsNoticeCard>
            ) : null}
            {accountNotice ? (
              <SettingsNoticeCard tone="neutral" className="mt-3">
                {accountNotice}
              </SettingsNoticeCard>
            ) : null}
            {accountError ? (
              <SettingsNoticeCard tone="warning-soft" className="mt-3">
                {accountError}
              </SettingsNoticeCard>
            ) : null}
            {!editingProviderName ? (
              <SettingsNoticeCard tone="muted" className="mt-3">
                {t("editor.providers.account.syncRequiresName")}
              </SettingsNoticeCard>
            ) : null}
          </div>
    </>
  );
}
