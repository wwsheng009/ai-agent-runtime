import { type CSSProperties } from "react";
import {
  MinusIcon,
  MonitorSmartphoneIcon,
  MoonIcon,
  PlusIcon,
  SparklesIcon,
  SunIcon,
  WavesIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  APP_FONT_SIZE_DEFAULT,
  CHAT_FONT_SIZE_DEFAULT,
  CODE_FONT_SIZE_DEFAULT,
  CODE_FONT_FAMILY_STACKS,
  FONT_SIZE_LIMITS,
  FONT_FAMILY_STACKS,
  formatFontSizePx,
  useAppSettings,
  type CodeFontPreset,
  type FontFamilyPreset,
} from "@/core/settings";
import { cn } from "@/lib/utils";

import { SettingsChoiceCard } from "./settings-choice-card";
import { editorControlClassName } from "./editor-control-class";
import { SettingsInfoCard } from "./settings-info-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

type FontChoiceCardProps = {
  active: boolean;
  description: string;
  label: string;
  onClick: () => void;
  sample: string;
  style?: CSSProperties;
};

function FontChoiceCard({
  active,
  description,
  label,
  onClick,
  sample,
  style,
}: FontChoiceCardProps) {
  return (
    <SettingsChoiceCard active={active} onClick={onClick}>
      <div className="text-base font-semibold text-[var(--foreground)]">{label}</div>
      <p className="mt-1.5 text-base leading-6 text-[var(--muted-foreground)]">
        {description}
      </p>
      <div
        style={style}
        className="mt-3 rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 text-base leading-6 text-[var(--foreground)]"
      >
        {sample}
      </div>
    </SettingsChoiceCard>
  );
}

type FontSizeControlCardProps = {
  defaultValue: number;
  description: string;
  title: string;
  value: number;
  onChange: (nextValue: number) => void;
};

function clampFontSizeValue(value: number) {
  return Math.min(
    FONT_SIZE_LIMITS.max,
    Math.max(FONT_SIZE_LIMITS.min, Math.round(value)),
  );
}

function FontSizeControlCard({
  defaultValue,
  description,
  title,
  value,
  onChange,
}: FontSizeControlCardProps) {
  const { t } = useTranslation("settings");
  const { t: tCommon } = useTranslation("common");
  const decrementDisabled = value <= FONT_SIZE_LIMITS.min;
  const incrementDisabled = value >= FONT_SIZE_LIMITS.max;

  function updateValue(nextValue: number) {
    onChange(clampFontSizeValue(nextValue));
  }

  return (
    <SettingsPanelCard
      title={<span className="text-base">{title}</span>}
      description={description}
      descriptionClassName="text-base"
      headerAside={
        <div className="rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2 py-0.5 font-mono app-text-11 text-[var(--foreground)]">
          {formatFontSizePx(value)}
        </div>
      }
      bodyClassName="grid gap-3"
    >
      <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2">
        <button
          type="button"
          disabled={decrementDisabled}
          onClick={() => updateValue(value - FONT_SIZE_LIMITS.step)}
          className={cn(
            "inline-flex h-9 w-9 items-center justify-center rounded-[0.7rem] border transition",
            decrementDisabled
              ? "cursor-not-allowed border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] opacity-50"
              : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
          )}
        >
          <MinusIcon size={16} />
        </button>
        <input
          type="range"
          min={FONT_SIZE_LIMITS.min}
          max={FONT_SIZE_LIMITS.max}
          step={FONT_SIZE_LIMITS.step}
          value={value}
          onChange={(event) => updateValue(Number(event.target.value))}
          className="h-2 w-full accent-[var(--accent-primary)]"
        />
        <button
          type="button"
          disabled={incrementDisabled}
          onClick={() => updateValue(value + FONT_SIZE_LIMITS.step)}
          className={cn(
            "inline-flex h-9 w-9 items-center justify-center rounded-[0.7rem] border transition",
            incrementDisabled
              ? "cursor-not-allowed border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] opacity-50"
              : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
          )}
        >
          <PlusIcon size={16} />
        </button>
      </div>

      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <label className="block">
          <span className="text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            {t("appearance.customPixels")}
          </span>
          <span className="mt-2 flex items-center gap-3">
            <input
              type="number"
              min={FONT_SIZE_LIMITS.min}
              max={FONT_SIZE_LIMITS.max}
              step={FONT_SIZE_LIMITS.step}
              value={value}
              onChange={(event) => {
                const nextValue = Number(event.target.value);
                if (Number.isFinite(nextValue)) {
                  updateValue(nextValue);
                }
              }}
              className={editorControlClassName}
            />
            <span className="shrink-0 text-base text-[var(--muted-foreground)]">
              px
            </span>
          </span>
        </label>

        <button
          type="button"
          onClick={() => updateValue(defaultValue)}
          disabled={value === defaultValue}
          className={cn(
            "rounded-[0.7rem] border px-3 py-2 text-base transition",
            value === defaultValue
              ? "cursor-not-allowed border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] opacity-50"
              : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
          )}
        >
          {tCommon("actions.reset")} {formatFontSizePx(defaultValue)}
        </button>
      </div>

      <div className="text-xs leading-5 text-[var(--muted-foreground)]">
        {t("appearance.sizeHint", {
          min: String(FONT_SIZE_LIMITS.min),
          max: String(FONT_SIZE_LIMITS.max),
        })}
      </div>
    </SettingsPanelCard>
  );
}

export function AppearanceSettingsPage() {
  const { t } = useTranslation("settings");
  const { resolvedTheme, settings, systemTheme, updateSection } = useAppSettings();
  const currentFontStack = FONT_FAMILY_STACKS[settings.appearance.fontFamily];
  const currentCodeFontStack =
    CODE_FONT_FAMILY_STACKS[settings.appearance.codeFontFamily];
  const themeValueLabel =
    settings.appearance.themeMode === "system"
      ? t("appearance.themeSystemResolved", {
          resolved:
            resolvedTheme === "dark"
              ? t("appearance.themeOptions.dark.label")
              : t("appearance.themeOptions.light.label"),
        })
      : t(`appearance.themeOptions.${settings.appearance.themeMode}.label`);

  const accentOptions = [
    {
      id: "gold",
      label: t("appearance.accentOptions.gold.label"),
      description: t("appearance.accentOptions.gold.description"),
      previewClassName: "from-[#f0c77b] to-[#d59645]",
    },
    {
      id: "cyan",
      label: t("appearance.accentOptions.cyan.label"),
      description: t("appearance.accentOptions.cyan.description"),
      previewClassName: "from-[#8fd0c6] to-[#51b7c2]",
    },
    {
      id: "violet",
      label: t("appearance.accentOptions.violet.label"),
      description: t("appearance.accentOptions.violet.description"),
      previewClassName: "from-[#9089fc] to-[#6f67f4]",
    },
  ] as const;

  const themeOptions = [
    {
      id: "system",
      label: t("appearance.themeOptions.system.label"),
      description: t("appearance.themeOptions.system.description"),
      icon: MonitorSmartphoneIcon,
    },
    {
      id: "light",
      label: t("appearance.themeOptions.light.label"),
      description: t("appearance.themeOptions.light.description"),
      icon: SunIcon,
    },
    {
      id: "dark",
      label: t("appearance.themeOptions.dark.label"),
      description: t("appearance.themeOptions.dark.description"),
      icon: MoonIcon,
    },
  ] as const;

  const fontFamilyOptions: Array<{
    description: string;
    id: FontFamilyPreset;
    label: string;
    sample: string;
  }> = [
    {
      id: "system",
      label: t("appearance.fontFamilyOptions.system.label"),
      description: t("appearance.fontFamilyOptions.system.description"),
      sample: t("appearance.fontFamilyOptions.system.sample"),
    },
    {
      id: "humanist",
      label: t("appearance.fontFamilyOptions.humanist.label"),
      description: t("appearance.fontFamilyOptions.humanist.description"),
      sample: t("appearance.fontFamilyOptions.humanist.sample"),
    },
    {
      id: "editorial",
      label: t("appearance.fontFamilyOptions.editorial.label"),
      description: t("appearance.fontFamilyOptions.editorial.description"),
      sample: t("appearance.fontFamilyOptions.editorial.sample"),
    },
  ];

  const codeFontOptions: Array<{
    description: string;
    id: CodeFontPreset;
    label: string;
    sample: string;
  }> = [
    {
      id: "jetbrains",
      label: t("appearance.codeFontOptions.jetbrains.label"),
      description: t("appearance.codeFontOptions.jetbrains.description"),
      sample: t("appearance.codeFontOptions.jetbrains.sample"),
    },
    {
      id: "cascadia",
      label: t("appearance.codeFontOptions.cascadia.label"),
      description: t("appearance.codeFontOptions.cascadia.description"),
      sample: t("appearance.codeFontOptions.cascadia.sample"),
    },
    {
      id: "classic",
      label: t("appearance.codeFontOptions.classic.label"),
      description: t("appearance.codeFontOptions.classic.description"),
      sample: t("appearance.codeFontOptions.classic.sample"),
    },
  ];

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("appearance.theme")}
        description={t("appearance.themeDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-3">
          {themeOptions.map((option) => {
            const active = settings.appearance.themeMode === option.id;
            const Icon = option.icon;

            return (
              <SettingsChoiceCard
                key={option.id}
                active={active}
                onClick={() =>
                  updateSection("appearance", { themeMode: option.id })
                }
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-base font-semibold text-[var(--foreground)]">
                      {option.label}
                    </div>
                    <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
                      {option.description}
                    </p>
                  </div>
                  <Icon
                    size={16}
                    className={cn(
                      active
                        ? "text-[var(--accent-primary)]"
                        : "text-[var(--muted-foreground)]",
                    )}
                  />
                </div>
                <div className="mt-3 rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-soft)] p-2.5">
                  <div className="flex items-center justify-between gap-3 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                    <span>{t("appearance.themeApplied")}</span>
                    <span className="text-[var(--foreground)]">
                      {option.id === "system"
                        ? t("appearance.themeSystemResolved", {
                            resolved:
                              systemTheme === "dark"
                                ? t("appearance.themeOptions.dark.label")
                                : t("appearance.themeOptions.light.label"),
                          })
                        : option.label}
                    </span>
                  </div>
                  <div
                    className={cn(
                      "mt-2.5 h-14 rounded-[0.7rem] border",
                      option.id === "dark" ||
                        (option.id === "system" && systemTheme === "dark")
                        ? "border-white/10 bg-[linear-gradient(180deg,#111318,#0c0d10)]"
                        : "border-slate-300/60 bg-[linear-gradient(180deg,#ffffff,#eef2f7)]",
                    )}
                  />
                </div>
              </SettingsChoiceCard>
            );
          })}
        </div>

      <SettingsInfoCard
        className="p-3"
        description={
          <>
              {t("appearance.currentlySetTo")}{" "}
              <span className="text-[var(--foreground)]">
                {themeValueLabel}
              </span>
              。
          </>
          }
        />
      </SettingsSection>

      <SettingsSection
        title={t("appearance.accent")}
        description={t("appearance.accentDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-3">
          {accentOptions.map((option) => {
            const active = settings.appearance.accentTone === option.id;

            return (
              <SettingsChoiceCard
                key={option.id}
                active={active}
                onClick={() =>
                  updateSection("appearance", { accentTone: option.id })
                }
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-base font-semibold text-[var(--foreground)]">
                      {option.label}
                    </div>
                    <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
                      {option.description}
                    </p>
                  </div>
                  <SparklesIcon
                    size={16}
                    className={cn(
                      active
                        ? "text-[var(--accent-primary)]"
                        : "text-[var(--muted-foreground)]",
                    )}
                  />
                </div>
                <div
                  className={cn(
                    "mt-3 h-14 rounded-[0.75rem] border border-[var(--border)] bg-gradient-to-r",
                    option.previewClassName,
                  )}
                />
              </SettingsChoiceCard>
            );
          })}
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("appearance.fontFamily")}
        description={t("appearance.fontFamilyDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-2">
          <div className="space-y-4">
            <div>
              <div className="text-sm font-semibold text-[var(--foreground)]">
                {t("appearance.bodyFont")}
              </div>
              <div className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
                {t("appearance.bodyFontDescription")}
              </div>
            </div>
            <div className="grid gap-3">
              {fontFamilyOptions.map((option) => (
                <FontChoiceCard
                  key={option.id}
                  active={settings.appearance.fontFamily === option.id}
                  description={option.description}
                  label={option.label}
                  sample={option.sample}
                  style={{ fontFamily: FONT_FAMILY_STACKS[option.id].sans }}
                  onClick={() =>
                    updateSection("appearance", { fontFamily: option.id })
                  }
                />
              ))}
            </div>
          </div>

          <div className="space-y-4">
            <div>
              <div className="text-sm font-semibold text-[var(--foreground)]">
                {t("appearance.codeFont")}
              </div>
              <div className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
                {t("appearance.codeFontDescription")}
              </div>
            </div>
            <div className="grid gap-3">
              {codeFontOptions.map((option) => (
                <FontChoiceCard
                  key={option.id}
                  active={settings.appearance.codeFontFamily === option.id}
                  description={option.description}
                  label={option.label}
                  sample={option.sample}
                  style={{ fontFamily: CODE_FONT_FAMILY_STACKS[option.id] }}
                  onClick={() =>
                    updateSection("appearance", { codeFontFamily: option.id })
                  }
                />
              ))}
            </div>
          </div>
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("appearance.size")}
        description={t("appearance.sizeDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-3">
          <FontSizeControlCard
            title={t("appearance.workspaceSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={APP_FONT_SIZE_DEFAULT}
            value={settings.appearance.textSize}
            onChange={(nextValue) =>
              updateSection("appearance", { textSize: nextValue })
            }
          />
          <FontSizeControlCard
            title={t("appearance.chatSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={CHAT_FONT_SIZE_DEFAULT}
            value={settings.appearance.chatTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { chatTextSize: nextValue })
            }
          />
          <FontSizeControlCard
            title={t("appearance.codeSample")}
            description={t("appearance.sizeDescription")}
            defaultValue={CODE_FONT_SIZE_DEFAULT}
            value={settings.appearance.codeTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { codeTextSize: nextValue })
            }
          />
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("appearance.motion")}
        description={t("appearance.motionDescription")}
      >
        <SettingsToggleCard
          checked={settings.appearance.reducedMotion}
          onChange={(checked) =>
            updateSection("appearance", {
              reducedMotion: checked,
            })
          }
          title={t("appearance.reducedMotion")}
          description={t("appearance.reducedMotionDescription")}
          icon={<WavesIcon size={16} />}
          iconWrapperClassName="text-[var(--accent-secondary)]"
        />
      </SettingsSection>

      <SettingsSection
        title={t("appearance.preview")}
        description={t("appearance.previewDescription")}
      >
        <div className="grid gap-4 xl:grid-cols-2">
          <SettingsInfoCard
            title={t("appearance.workspaceSample")}
            description={t("appearance.previewSampleText")}
            className="font-sans"
            contentClassName="space-y-3"
          >
            <div
              className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3"
              style={{ fontSize: `${settings.appearance.textSize}px` }}
            >
              <div className="text-sm leading-7 text-[var(--foreground)]">
                {t("appearance.previewWorkspaceBody")}
              </div>
            </div>
          </SettingsInfoCard>
          <SettingsInfoCard
            title={t("appearance.codeSample")}
            description={t("appearance.currentCodeStack")}
            contentClassName="space-y-3"
          >
            <div
              className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3 font-mono"
              style={{ fontSize: `${settings.appearance.codeTextSize}px` }}
            >
              <div className="whitespace-pre-wrap leading-6 text-[var(--foreground)]">
                {`const workspace = await runtime.openThread("new");\nawait workspace.ask("Summarize the failing trace and propose a fix.");\nreturn workspace.receipts.latest();`}
              </div>
            </div>
            <div className="grid gap-2 text-sm leading-6 text-[var(--muted-foreground)]">
              <div>
                {t("appearance.currentUIStack")} {currentFontStack.sans}
              </div>
              <div>
                {t("appearance.currentSerifStack")} {currentFontStack.serif}
              </div>
              <div>
                {t("appearance.currentCodeStack")} {currentCodeFontStack}
              </div>
            </div>
          </SettingsInfoCard>
        </div>
      </SettingsSection>
    </div>
  );
}
