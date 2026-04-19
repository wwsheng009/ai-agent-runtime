import { type CSSProperties } from "react";
import {
  MinusIcon,
  GaugeCircleIcon,
  MonitorSmartphoneIcon,
  MoonIcon,
  PlusIcon,
  SparklesIcon,
  SunIcon,
  WavesIcon,
} from "lucide-react";

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

const accentOptions = [
  {
    id: "gold",
    label: "Amber relay",
    description: "保持当前工作区的金色高亮基调。",
    previewClassName: "from-[#f0c77b] to-[#d59645]",
  },
  {
    id: "cyan",
    label: "Cool signal",
    description: "把主强调色切换成更偏系统状态的冷色。",
    previewClassName: "from-[#8fd0c6] to-[#51b7c2]",
  },
  {
    id: "violet",
    label: "Route focus",
    description: "使用更靠近路由与规划语义的紫色高亮。",
    previewClassName: "from-[#9089fc] to-[#6f67f4]",
  },
] as const;

const themeOptions = [
  {
    id: "system",
    label: "跟随系统",
    description: "监听系统深浅色偏好，在系统切换时自动同步。",
    icon: MonitorSmartphoneIcon,
  },
  {
    id: "light",
    label: "浅色",
    description: "适合白天、文档阅读和明亮环境。",
    icon: SunIcon,
  },
  {
    id: "dark",
    label: "深色",
    description: "适合终端式工作流、夜间和低光环境。",
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
    label: "System UI",
    description: "Segoe UI / Helvetica Neue / system-ui",
    sample: "Operational detail stays readable during long sessions.",
  },
  {
    id: "humanist",
    label: "Readable humanist",
    description: "Trebuchet MS / Verdana / Palatino",
    sample: "Comfortable for dense workspace copy and settings text.",
  },
  {
    id: "editorial",
    label: "Editorial modern",
    description: "Aptos / Cambria / Georgia",
    sample: "Softer rhythm for landing copy and long-form reading.",
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
    label: "JetBrains stack",
    description: "JetBrains Mono / Cascadia Code / Consolas",
    sample: "const traceId = receipts.latest()?.trace_id ?? \"\";",
  },
  {
    id: "cascadia",
    label: "Cascadia stack",
    description: "Cascadia Code / JetBrains Mono / Consolas",
    sample: "await runtime.follow({ requestId, sessionId });",
  },
  {
    id: "classic",
    label: "Classic console",
    description: "Consolas / SFMono / Menlo / Monaco",
    sample: "if (event.level === \"error\") return halt(event);",
  },
];

const previewCode = [
  "const workspace = await runtime.openThread(\"new\");",
  "await workspace.ask(\"Summarize the failing trace and propose a fix.\");",
  "return workspace.receipts.latest();",
].join("\n");

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
              自定义像素值
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
            恢复默认 {formatFontSizePx(defaultValue)}
          </button>
        </div>

        <div className="text-xs leading-5 text-[var(--muted-foreground)]">
          支持直接输入具体字号，范围 {FONT_SIZE_LIMITS.min}-
          {FONT_SIZE_LIMITS.max}px。
        </div>
    </SettingsPanelCard>
  );
}

export function AppearanceSettingsPage() {
  const { resolvedTheme, settings, systemTheme, updateSection } = useAppSettings();
  const currentFontStack = FONT_FAMILY_STACKS[settings.appearance.fontFamily];
  const currentCodeFontStack =
    CODE_FONT_FAMILY_STACKS[settings.appearance.codeFontFamily];

  return (
    <div className="space-y-6">
      <SettingsSection
        title="主题"
        description="控制整个前端界面的深浅色模式，包括工作区、设置面板和首页。"
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
                    <span>实际生效</span>
                    <span className="text-[var(--foreground)]">
                      {option.id === "system"
                        ? systemTheme === "dark"
                          ? "System -> Dark"
                          : "System -> Light"
                        : option.id === "dark"
                          ? "Dark"
                          : "Light"}
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
              当前设置为{" "}
              <span className="text-[var(--foreground)]">
                {settings.appearance.themeMode === "system"
                  ? `跟随系统（当前解析为 ${resolvedTheme === "dark" ? "深色" : "浅色"}）`
                  : settings.appearance.themeMode === "dark"
                    ? "深色"
                    : "浅色"}
              </span>
              。
            </>
          }
        />
      </SettingsSection>

      <SettingsSection
        title="强调色"
        description="影响设置弹窗、主操作按钮以及工作区里已接入变量的高亮色。"
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
        title="字体族"
        description="控制整个系统的界面字体，以及代码块、日志、配置编辑器等单宽字体栈。"
      >
        <div className="grid gap-4 xl:grid-cols-2">
          <div className="space-y-4">
            <div>
              <div className="text-sm font-semibold text-[var(--foreground)]">
                界面与正文
              </div>
              <div className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
                会统一影响首页、工作区、设置面板，以及使用 `font-serif` 的展示标题。
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
                代码与日志
              </div>
              <div className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
                会应用到代码块、日志 JSON 预览，以及使用单宽编辑样式的文本区域。
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
        title="字号"
        description="整体字号控制全站基础文字节奏；聊天和代码字号会覆盖各自的高频阅读区域。现在支持直接输入自定义 px 值，不再限制在固定几档 preset。"
      >
        <div className="grid gap-4 xl:grid-cols-3">
          <FontSizeControlCard
            title="整体文字大小"
            description="影响整个前端系统的基础字号层级。"
            defaultValue={APP_FONT_SIZE_DEFAULT}
            value={settings.appearance.textSize}
            onChange={(nextValue) =>
              updateSection("appearance", { textSize: nextValue })
            }
          />
          <FontSizeControlCard
            title="聊天文字大小"
            description="影响消息正文和输入框的主要阅读字号。"
            defaultValue={CHAT_FONT_SIZE_DEFAULT}
            value={settings.appearance.chatTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { chatTextSize: nextValue })
            }
          />
          <FontSizeControlCard
            title="代码文字大小"
            description="影响代码块、日志预览和单宽编辑器的字号。"
            defaultValue={CODE_FONT_SIZE_DEFAULT}
            value={settings.appearance.codeTextSize}
            onChange={(nextValue) =>
              updateSection("appearance", { codeTextSize: nextValue })
            }
          />
        </div>
      </SettingsSection>

      <SettingsSection
        title="动效"
        description="适合在远程桌面、录屏或你希望界面更克制时开启。"
      >
        <SettingsToggleCard
          checked={settings.appearance.reducedMotion}
          onChange={(checked) =>
            updateSection("appearance", {
              reducedMotion: checked,
            })
          }
          title="减少动画"
          description="关闭脉冲、漂浮和大部分过渡动画，同时让滚动行为回到即时模式。"
          icon={<WavesIcon size={16} />}
          iconWrapperClassName="text-[var(--accent-secondary)]"
        />
      </SettingsSection>

      <SettingsSection
        title="实时预览"
        description="下面这段示例会跟随你当前选择的字体族和字号即时刷新。"
      >
        <div className="grid gap-4 xl:grid-cols-[minmax(0,1.2fr)_minmax(0,0.8fr)]">
          <SettingsPanelCard
            title="工作区正文预览"
            icon={<GaugeCircleIcon size={16} className="text-[var(--accent-primary)]" />}
          >
            <div className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3.5 py-3">
              <div className="text-sm font-semibold text-[var(--foreground)]">
                Workspace reading sample
              </div>
              <p className="app-chat-copy mt-3 text-[var(--foreground)]">
                Use the current chat font settings to review plans, receipts, streamed
                output, and teammate updates without straining during long sessions.
              </p>
            </div>
            <div className="mt-3 grid gap-2.5 sm:grid-cols-3">
              <div className="rounded-[0.75rem] border border-[var(--border)] bg-black/10 px-3 py-2.5">
                <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                  整体字号
                </div>
                <div className="mt-2 font-mono text-sm text-[var(--foreground)]">
                  {formatFontSizePx(settings.appearance.textSize)}
                </div>
              </div>
              <div className="rounded-[0.75rem] border border-[var(--border)] bg-black/10 px-3 py-2.5">
                <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                  聊天字号
                </div>
                <div className="mt-2 font-mono text-sm text-[var(--foreground)]">
                  {formatFontSizePx(settings.appearance.chatTextSize)}
                </div>
              </div>
              <div className="rounded-[0.75rem] border border-[var(--border)] bg-black/10 px-3 py-2.5">
                <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                  代码字号
                </div>
                <div className="mt-2 font-mono text-sm text-[var(--foreground)]">
                  {formatFontSizePx(settings.appearance.codeTextSize)}
                </div>
              </div>
            </div>
          </SettingsPanelCard>

          <SettingsPanelCard
            title="代码与字体栈预览"
            icon={<SparklesIcon size={16} className="text-[var(--accent-secondary)]" />}
          >
            <pre className="app-code-surface overflow-x-auto whitespace-pre-wrap rounded-[0.75rem] border border-[var(--border)] bg-black/20 p-3 font-mono text-[var(--foreground)]">
              {previewCode}
            </pre>
            <div className="mt-3 space-y-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
              <div>
                <span className="text-[var(--foreground)]">当前界面字体栈：</span>
                {" "}
                {currentFontStack.sans}
              </div>
              <div>
                <span className="text-[var(--foreground)]">当前标题衬线栈：</span>
                {" "}
                {currentFontStack.serif}
              </div>
              <div>
                <span className="text-[var(--foreground)]">当前代码字体栈：</span>
                {" "}
                {currentCodeFontStack}
              </div>
            </div>
          </SettingsPanelCard>
        </div>
      </SettingsSection>

      <SettingsSection
        title="当前生效范围"
        description="这批设置是本地偏好，不会写入后端配置文件。"
      >
        <div className="grid gap-3 md:grid-cols-2">
          <SettingsInfoCard
            title="即时生效"
            icon={<GaugeCircleIcon size={16} className="text-[var(--accent-primary)]" />}
            description="主题、强调色、字体族、字号和动效会在当前标签页立即更新。"
          />
          <SettingsInfoCard
            title="浏览器持久化"
            icon={<SparklesIcon size={16} className="text-[var(--accent-secondary)]" />}
            description="关闭页面后，下次打开首页、工作区和日志页仍会保持这套本地排版设置。"
          />
        </div>
      </SettingsSection>
    </div>
  );
}
