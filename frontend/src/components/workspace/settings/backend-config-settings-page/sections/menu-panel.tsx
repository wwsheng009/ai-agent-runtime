// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { MenuButton } from "../primitives";
import { type EditorMode } from "../types";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorMenuPanel({ core }: { core: ConfigEditorCore }) {
  const {
    isModeSwitching,
    mode,
    switchMode,
    t,
    translatedModeMenuEntries,
  } = core;

  return (
    <div className="min-w-0 space-y-2.5 lg:sticky lg:top-[8.5rem] lg:self-start">
      <label className="block rounded-panel border border-border bg-surface-softer p-3 lg:hidden">
        <span className="block text-sm font-semibold text-foreground">
          {t("editor.panels.modeTitle")}
        </span>
        <span className="mt-1 block text-xs leading-5 text-muted-foreground">
          {t("editor.panels.modeDescription")}
        </span>
        <select
          value={mode}
          disabled={isModeSwitching}
          aria-label={t("editor.panels.modeTitle")}
          onChange={(event) => {
            void switchMode(event.target.value as EditorMode);
          }}
          className="mt-3 h-10 w-full rounded-field border border-border bg-surface-solid px-3 text-sm text-foreground outline-none transition focus:border-accent-primary-border focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
        >
          {translatedModeMenuEntries.map((entry) => (
            <option key={entry.mode} value={entry.mode}>
              {entry.label}
            </option>
          ))}
        </select>
      </label>

      <div className="hidden lg:block">
        <div className="rounded-panel border border-border bg-surface-softer p-3">
          <div className="flex items-center gap-2">
            <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("editor.panels.modeTitle")}
            </div>
          </div>
          <div className="mt-3 grid gap-2">
            {translatedModeMenuEntries.map((entry) => (
              <MenuButton
                key={entry.mode}
                active={mode === entry.mode}
                badge={core.getModeBadge(entry.mode)}
                description={entry.description}
                disabled={isModeSwitching}
                icon={entry.icon}
                label={entry.label}
                onClick={() => {
                  void switchMode(entry.mode);
                }}
              />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
