// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { MenuButton } from "../primitives";
import { Select } from "@/components/ui/select";
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
        <Select
          ariaLabel={t("editor.panels.modeTitle")}
          className="mt-3 w-full"
          disabled={isModeSwitching}
          onChange={(nextMode) => {
            void switchMode(nextMode as EditorMode);
          }}
          options={translatedModeMenuEntries.map((entry) => ({
            value: entry.mode,
            label: entry.label,
          }))}
          size="md"
          surface="soft"
          value={mode}
        />
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
