import {
  BellIcon,
  InfoIcon,
  PaletteIcon,
  Settings2Icon,
  SlidersHorizontalIcon,
  XIcon,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { useAppSettings } from "@/core/settings";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import { type RuntimeClientIdentity } from "@/lib/runtime-client";
import { type RuntimeTeamRecord } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { AboutSettingsPage } from "./about-settings-page";
import { AppearanceSettingsPage } from "./appearance-settings-page";
import { ChatSettingsPage } from "./chat-settings-page";
import { LocalizationSettings } from "./localization-settings";
import { NotificationSettingsPage } from "./notification-settings-page";
import { WorkspaceSettingsPage } from "./workspace-settings-page";

export type SettingsSectionId =
  | "appearance"
  | "workspace"
  | "chat"
  | "notifications"
  | "about";

type SettingsDialogProps = {
  defaultSection?: SettingsSectionId;
  modelOptions: string[];
  onClose: () => void;
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  open: boolean;
  providerOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  runtimeClient: RuntimeClientIdentity;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeTeams: RuntimeTeamRecord[];
  onResetRuntimeClientIdentity: () => void;
  selectedModel: string;
  selectedProvider: string;
};

type SettingsNavItem = {
  description: string;
  icon: LucideIcon;
  id: SettingsSectionId;
  label: string;
};

export function SettingsDialog({
  defaultSection = "appearance",
  modelOptions,
  onClose,
  onModelChange,
  onProviderChange,
  open,
  providerOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  runtimeClient,
  runtimeSessionsSummary,
  runtimeTeams,
  onResetRuntimeClientIdentity,
  selectedModel,
  selectedProvider,
}: SettingsDialogProps) {
  const { resetSettings } = useAppSettings();
  const { t } = useTranslation("settings");
  const [activeSection, setActiveSection] =
    useState<SettingsSectionId>(defaultSection);

  useEffect(() => {
    if (!open) {
      return;
    }

    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) {
        setActiveSection(defaultSection);
      }
    });

    return () => {
      cancelled = true;
    };
  }, [defaultSection, open]);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  const sections = useMemo<SettingsNavItem[]>(
    () => [
      {
        id: "appearance",
        label: t("sections.appearance.label"),
        description: t("sections.appearance.description"),
        icon: PaletteIcon,
      },
      {
        id: "workspace",
        label: t("sections.workspace.label"),
        description: t("sections.workspace.description"),
        icon: Settings2Icon,
      },
      {
        id: "chat",
        label: t("sections.chat.label"),
        description: t("sections.chat.description"),
        icon: SlidersHorizontalIcon,
      },
      {
        id: "notifications",
        label: t("sections.notifications.label"),
        description: t("sections.notifications.description"),
        icon: BellIcon,
      },
      {
        id: "about",
        label: t("sections.about.label"),
        description: t("sections.about.description"),
        icon: InfoIcon,
      },
    ],
    [t],
  );

  if (!open) {
    return null;
  }

  return (
    <div
      className="fixed inset-0 z-[120] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div className="flex max-h-[calc(100vh-1.5rem)] w-full max-w-6xl flex-col overflow-hidden rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        <div className="flex items-start justify-between gap-3 px-3.5 py-3 sm:px-4">
          <div>
            <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--accent-primary)]">
              {t("dialog.eyebrow")}
            </div>
            <h2 className="mt-1 text-lg font-semibold tracking-[-0.03em] text-[var(--foreground)]">
              {t("dialog.title")}
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-[var(--muted-foreground)]">
              {t("dialog.description")}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Link
              to="/runtime/config"
              className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              onClick={onClose}
            >
              {t("dialog.backendConfig")}
            </Link>
            <Button variant="ghost" size="sm" onClick={resetSettings}>
              {t("dialog.resetFrontendDefaults")}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onClose}
              aria-label={t("dialog.close")}
            >
              <XIcon size={16} />
            </Button>
          </div>
        </div>

        <div className="grid min-h-0 flex-1 gap-0 border-t border-[var(--border)] md:grid-cols-[208px_minmax(0,1fr)]">
          <nav className="min-h-0 overflow-y-auto border-b border-[var(--border)] p-2 md:border-b-0 md:border-r">
            <div className="space-y-1">
              {sections.map((section) => {
                const active = activeSection === section.id;
                const Icon = section.icon;

                return (
                  <button
                    key={section.id}
                    type="button"
                    onClick={() => setActiveSection(section.id)}
                    className={cn(
                      "w-full rounded-[0.8rem] border px-3 py-2.5 text-left transition",
                      active
                        ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
                        : "border-transparent bg-transparent hover:border-[var(--border)] hover:bg-[var(--surface-softer)]",
                    )}
                  >
                    <div className="flex items-center gap-3">
                      <span
                        className={cn(
                          "inline-flex size-7 items-center justify-center rounded-[0.65rem] border",
                          active
                            ? "border-[var(--accent-primary-border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]"
                            : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]",
                        )}
                      >
                        <Icon size={15} />
                      </span>
                      <div className="min-w-0">
                        <div className="text-base font-semibold text-[var(--foreground)]">
                          {section.label}
                        </div>
                        <div className="mt-0.5 text-xs leading-5 text-[var(--muted-foreground)]">
                          {section.description}
                        </div>
                      </div>
                    </div>
                  </button>
                );
              })}
            </div>
          </nav>

          <div className="min-h-0 overflow-y-auto px-3.5 py-3.5 sm:px-4">
            <LocalizationSettings />
            {activeSection === "appearance" ? <AppearanceSettingsPage /> : null}
            {activeSection === "workspace" ? <WorkspaceSettingsPage /> : null}
            {activeSection === "chat" ? (
              <ChatSettingsPage
                modelOptions={modelOptions}
                onModelChange={onModelChange}
                onProviderChange={onProviderChange}
                providerOptions={providerOptions}
                runtimeModelsError={runtimeModelsError}
                runtimeModelsLoading={runtimeModelsLoading}
                selectedModel={selectedModel}
                selectedProvider={selectedProvider}
              />
            ) : null}
            {activeSection === "notifications" ? <NotificationSettingsPage /> : null}
            {activeSection === "about" ? (
              <AboutSettingsPage
                providerOptions={providerOptions}
                runtimeClient={runtimeClient}
                runtimeSessionsSummary={runtimeSessionsSummary}
                runtimeTeams={runtimeTeams}
                onResetRuntimeClientIdentity={onResetRuntimeClientIdentity}
                selectedModel={selectedModel}
                selectedProvider={selectedProvider}
              />
            ) : null}
          </div>
        </div>

        <div className="border-t border-[var(--border)] px-3.5 py-2.5 text-xs leading-5 text-[var(--muted-foreground)] sm:px-4">
          {t("dialog.localStorageFooter")}
        </div>
      </div>
    </div>
  );
}
