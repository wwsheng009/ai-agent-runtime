import {
  BotIcon,
  CheckIcon,
  CopyIcon,
  MoreHorizontalIcon,
  PencilIcon,
  RefreshCwIcon,
  SparklesIcon,
  StarIcon,
  Trash2Icon,
} from "lucide-react";
import { createPortal } from "react-dom";
import { useEffect, useMemo, useRef, useState } from "react";
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
  const protocolOptions = useMemo(
    () =>
      [...new Set(providers.map((provider) => provider.protocol).filter(Boolean))]
        .sort((left, right) => left.localeCompare(right))
        .map((protocol) => ({ value: protocol, label: protocol })),
    [providers],
  );

  return (
      <ConfigDomainTable
        title={t("editor.providers.title")}
        titleIcon={BotIcon}
        items={providers}
        getRowKey={(provider) => provider.name}
        searchable
        getSearchText={providerSearchText}
        pageSize={10}
        emptyState={t("editor.providers.emptyState")}
        filters={[
          {
            key: "status",
            label: t("editor.providers.filters.status"),
            getValue: (provider) =>
              provider.enabled ? "enabled" : "disabled",
            options: [
              {
                value: "enabled",
                label: t("editor.providers.row.enabled"),
              },
              {
                value: "disabled",
                label: t("editor.providers.row.disabled"),
              },
            ],
          },
          {
            key: "protocol",
            label: t("editor.providers.filters.protocol"),
            getValue: (provider) => provider.protocol,
            options: protocolOptions,
          },
        ]}
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
            sortKey: "name",
            sortValue: (provider) => provider.name,
            className: "min-w-[11rem]",
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
            sortKey: "protocol",
            sortValue: (provider) => provider.protocol,
            className: "min-w-[8rem]",
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
            sortKey: "defaultModel",
            sortValue: (provider) => provider.defaultModel,
            className: "min-w-[10rem]",
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
            sortKey: "siteType",
            sortValue: (provider) => provider.siteType,
            className: "min-w-[13rem]",
            cell: (provider) => (
              <div className="min-w-[12rem] max-w-[18rem]">
                <div className="flex min-w-0 gap-2">
                  {provider.siteType ? (
                    <Badge className="max-w-full truncate whitespace-nowrap">
                      {provider.siteType}
                      {provider.siteTypeConfidence
                        ? ` · ${provider.siteTypeConfidence}`
                        : ""}
                    </Badge>
                  ) : (
                    <Badge>{t("editor.providers.row.siteUndetected")}</Badge>
                  )}
                </div>
                <div
                  className="mt-1 line-clamp-2 break-all text-xs text-muted-foreground"
                  title={
                    provider.accountSummary ||
                    (provider.accountAuthRef
                      ? t("editor.providers.row.authRef", {
                          ref: provider.accountAuthRef,
                        })
                      : t("editor.providers.row.noAccountCache"))
                  }
                >
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
            sortKey: "status",
            sortValue: (provider) => Number(provider.enabled),
            className: "min-w-[9rem]",
            cell: (provider) => (
              <div className="flex min-w-[8rem] flex-nowrap items-center gap-1.5 whitespace-nowrap">
                <Badge className="shrink-0 whitespace-nowrap">
                  {provider.enabled
                    ? t("editor.providers.row.enabled")
                    : t("editor.providers.row.disabled")}
                </Badge>
                {provider.hasProxyOverride ? (
                  <span
                    title={
                      provider.proxyEnabled
                        ? t("editor.providers.row.proxyOverride")
                        : t("editor.providers.row.proxyConfigured")
                    }
                  >
                    <Badge className="shrink-0 whitespace-nowrap">
                      {provider.proxyEnabled
                        ? t("editor.providers.row.proxyOverride")
                        : t("editor.providers.row.proxyConfigured")}
                    </Badge>
                  </span>
                ) : null}
                {provider.extraFieldCount > 0 ? (
                  <span
                    title={t("editor.providers.row.extraFields", {
                      count: provider.extraFieldCount,
                    })}
                  >
                    <Badge className="shrink-0 whitespace-nowrap px-1.5">
                      {t("editor.providers.row.extraFieldsCompact", {
                        count: provider.extraFieldCount,
                      })}
                    </Badge>
                  </span>
                ) : null}
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.actions"),
            cell: (provider) => (
              <SettingsActionGroup compact className="items-center">
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
                  label={t("editor.providers.actions.edit", { name: provider.name })}
                  onClick={() => onEditProvider(provider)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <ProviderActionsMenu
                  copied={copiedProviderName === provider.name}
                  disabled={rowBusyName === provider.name}
                  onCopy={() => onCopyProvider(provider)}
                  onDelete={() => onDeleteProvider(provider.name)}
                  onRefresh={() => onRefreshProviderAccount(provider.name)}
                  providerName={provider.name}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            sticky: "right",
            className: "w-[7.5rem] min-w-[7.5rem]",
          },
        ]}
      />
  );
}

function ProviderActionsMenu({
  copied,
  disabled,
  onCopy,
  onDelete,
  onRefresh,
  providerName,
}: {
  copied: boolean;
  disabled: boolean;
  onCopy: () => void;
  onDelete: () => void;
  onRefresh: () => void;
  providerName: string;
}) {
  const { t } = useTranslation("runtimeConfig");
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 8, top: 8 });
  const triggerRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }

    function updatePosition() {
      const rect = triggerRef.current?.getBoundingClientRect();
      if (!rect) {
        return;
      }
      const menuWidth = 208;
      const menuHeight = menuRef.current?.getBoundingClientRect().height ?? 148;
      const belowTrigger = rect.bottom + 4;
      setPosition({
        left: Math.max(
          8,
          Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - 8),
        ),
        top:
          belowTrigger + menuHeight <= window.innerHeight - 8
            ? belowTrigger
            : Math.max(8, rect.top - menuHeight - 4),
      });
    }

    function handlePointerDown(event: PointerEvent) {
      const target = event.target as Node;
      if (
        !triggerRef.current?.contains(target) &&
        !menuRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    }

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(false);
        triggerRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
        return;
      }
      const menuItems = Array.from(
        menuRef.current?.querySelectorAll<HTMLButtonElement>(
          '[role="menuitem"]:not([disabled])',
        ) ?? [],
      );
      const currentIndex = menuItems.indexOf(
        document.activeElement as HTMLButtonElement,
      );
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const direction = event.key === "ArrowDown" ? 1 : -1;
        const nextIndex =
          currentIndex < 0
            ? 0
            : (currentIndex + direction + menuItems.length) % menuItems.length;
        menuItems[nextIndex]?.focus();
      } else if (event.key === "Home") {
        event.preventDefault();
        menuItems[0]?.focus();
      } else if (event.key === "End") {
        event.preventDefault();
        menuItems.at(-1)?.focus();
      }
    }

    updatePosition();
    menuRef.current
      ?.querySelector<HTMLButtonElement>('[role="menuitem"]')
      ?.focus();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open]);

  function runAction(action: () => void) {
    action();
    setOpen(false);
    triggerRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }

  const menu = open
    ? createPortal(
        <div
          ref={menuRef}
          role="menu"
          aria-label={t("editor.providers.actions.menu", { name: providerName })}
          className="fixed z-[200] max-h-[min(60vh,16rem)] w-52 overflow-y-auto rounded-card border border-border bg-surface-overlay p-1.5 shadow-[0_10px_24px_rgba(0,0,0,0.24)]"
          style={{ left: position.left, top: position.top }}
        >
          <button
            type="button"
            role="menuitem"
            disabled={disabled}
            className="flex w-full items-center gap-2 rounded-control px-2 py-2 text-left text-sm text-foreground transition hover:bg-surface-soft disabled:cursor-not-allowed disabled:opacity-50"
            onClick={() => runAction(onRefresh)}
          >
            <RefreshCwIcon
              size={14}
              className={disabled ? "animate-spin" : undefined}
            />
            {t("editor.providers.actions.refreshBalance", {
              name: providerName,
            })}
          </button>
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2 rounded-control px-2 py-2 text-left text-sm text-foreground transition hover:bg-surface-soft"
            onClick={() => runAction(onCopy)}
          >
            {copied ? <CheckIcon size={14} /> : <CopyIcon size={14} />}
            {t("editor.providers.actions.copyConfig", {
              name: providerName,
            })}
          </button>
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2 rounded-control px-2 py-2 text-left text-sm text-analytics-danger transition hover:bg-surface-soft"
            onClick={() => runAction(onDelete)}
          >
            <Trash2Icon size={14} />
            {t("editor.providers.actions.delete", { name: providerName })}
          </button>
        </div>,
        document.body,
      )
    : null;

  return (
    <div ref={triggerRef} className="shrink-0">
      <SettingsIconActionButton
        label={t("editor.providers.actions.openMenu", { name: providerName })}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        <MoreHorizontalIcon size={14} />
      </SettingsIconActionButton>
      {menu}
    </div>
  );
}
