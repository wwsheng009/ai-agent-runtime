import { Suspense } from "react";
import { type ShellDomain } from "../../domains/shell";
import { RuntimeShellDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function ShellModeSection({ core, domain }: { core: ConfigEditorCore; domain: ShellDomain }) {
  const { shellConfig, t } = core;

  const { handleShellConfigChange } = domain;

  return (
    <Suspense
      fallback={<ConfigEditorLoadingCard label={t("editor.modes.shell.label")} />}
    >
      <RuntimeShellDomainEditor
        config={shellConfig}
        onChange={handleShellConfigChange}
      />
    </Suspense>
  );
}
