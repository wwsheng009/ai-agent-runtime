import {
  BotIcon,
  CheckIcon,
  CopyIcon,
  PencilIcon,
  RefreshCwIcon,
  SparklesIcon,
  StarIcon,
  Trash2Icon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";
import {
  SettingsActionGroup,
  SettingsIconActionButton,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

import { providerSearchText } from "./draft-utils";

type ProviderTableProps = {
  copiedProviderName: string | null;
  defaultProvider: string;
  enabledCount: number;
  onCopyProvider: (provider: RuntimeProviderSummary) => void;
  onCreateProvider: () => void;
  onImportProvider: () => void;
  onDeleteProvider: (name: string) => void;
  onEditProvider: (provider: RuntimeProviderSummary) => void;
  onRefreshProviderAccount: (name: string) => void;
  onSetDefaultProvider: (name: string) => void;
  providers: RuntimeProviderSummary[];
  rowBusyName: string | null;
};

export function ProviderTable({
  copiedProviderName,
  defaultProvider,
  enabledCount,
  onCopyProvider,
  onCreateProvider,
  onImportProvider,
  onDeleteProvider,
  onEditProvider,
  onRefreshProviderAccount,
  onSetDefaultProvider,
  providers,
  rowBusyName,
}: ProviderTableProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
      <ConfigDomainTable
        title={t("editor.providers.title")}
        titleIcon={BotIcon}
        description={t("editor.providers.description")}
        items={providers}
        getRowKey={(provider) => provider.name}
        searchable
        getSearchText={providerSearchText}
        pageSize={10}
        emptyState={t("editor.providers.emptyState")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.providers.summary.total", { count: providers.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.providers.summary.enabled", { count: enabledCount })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {defaultProvider
                ? t("editor.providers.summary.defaultNamed", { name: defaultProvider })
                : t("editor.providers.summary.noDefault")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={onImportProvider}>
              <SparklesIcon size={14} />
              {t("editor.providers.import.action")}
            </Button>
            <SettingsAddButton
              size="sm"
              label={t("editor.providers.create")}
              onClick={onCreateProvider}
            />
          </>
        }
        columns={[
          {
            header: t("editor.providers.columns.name"),
            cell: (provider) => (
              <div className="min-w-[11rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <div className="font-semibold text-foreground">
                    {provider.name}
                  </div>
                  {provider.name === defaultProvider ? (
                    <Badge>{t("editor.providers.row.defaultBadge")}</Badge>
                  ) : null}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {provider.baseUrl || t("editor.providers.row.noBaseUrl")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.protocol"),
            cell: (provider) => (
              <div className="min-w-[7rem]">
                <div>{provider.protocol || "--"}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {provider.supportTypes.join(", ") || t("editor.providers.row.noSupportTypes")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.defaultModel"),
            cell: (provider) => (
              <div className="min-w-[10rem]">
                <div>{provider.defaultModel || "--"}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {t("editor.providers.row.modelCount", {
                    count: provider.supportedModels.length,
                  })}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.siteBalance"),
            cell: (provider) => (
              <div className="min-w-[12rem]">
                <div className="flex flex-wrap gap-2">
                  {provider.siteType ? (
                    <Badge>
                      {provider.siteType}
                      {provider.siteTypeConfidence
                        ? ` · ${provider.siteTypeConfidence}`
                        : ""}
                    </Badge>
                  ) : (
                    <Badge>{t("editor.providers.row.siteUndetected")}</Badge>
                  )}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {provider.accountSummary ||
                    (provider.accountAuthRef
                      ? t("editor.providers.row.authRef", {
                          ref: provider.accountAuthRef,
                        })
                      : t("editor.providers.row.noAccountCache"))}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.status"),
            cell: (provider) => (
              <div className="flex flex-wrap gap-2">
                <Badge>
                  {provider.enabled
                    ? t("editor.providers.row.enabled")
                    : t("editor.providers.row.disabled")}
                </Badge>
                {provider.hasProxyOverride ? (
                  <Badge>
                    {provider.proxyEnabled
                      ? t("editor.providers.row.proxyOverride")
                      : t("editor.providers.row.proxyConfigured")}
                  </Badge>
                ) : null}
                {provider.extraFieldCount > 0 ? (
                  <Badge>
                    {t("editor.providers.row.extraFields", {
                      count: provider.extraFieldCount,
                    })}
                  </Badge>
                ) : null}
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.actions"),
            cell: (provider) => (
              <SettingsActionGroup compact>
                {provider.name !== defaultProvider ? (
                  <SettingsIconActionButton
                    label={t("editor.providers.actions.setDefault", {
                      name: provider.name,
                    })}
                    onClick={() => onSetDefaultProvider(provider.name)}
                  >
                    <StarIcon size={13} />
                  </SettingsIconActionButton>
                ) : null}
                <SettingsIconActionButton
                  label={t("editor.providers.actions.refreshBalance", {
                    name: provider.name,
                  })}
                  onClick={() => void onRefreshProviderAccount(provider.name)}
                  disabled={rowBusyName === provider.name}
                >
                  <RefreshCwIcon
                    size={13}
                    className={
                      rowBusyName === provider.name ? "animate-spin" : undefined
                    }
                  />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.copyConfig", {
                    name: provider.name,
                  })}
                  onClick={() => void onCopyProvider(provider)}
                >
                  {copiedProviderName === provider.name ? (
                    <CheckIcon size={13} />
                  ) : (
                    <CopyIcon size={13} />
                  )}
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.edit", { name: provider.name })}
                  onClick={() => onEditProvider(provider)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.delete", { name: provider.name })}
                  onClick={() => onDeleteProvider(provider.name)}
                >
                  <Trash2Icon size={13} />
                </SettingsIconActionButton>
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[11.5rem] min-w-[11.5rem]",
          },
        ]}
      />
  );
}
