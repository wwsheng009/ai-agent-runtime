import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RuntimeShellConfigSummary } from "../../runtime-shell-domain-utils";
import { type ConfigEditorCore } from "../use-config-core";

/**
 * shell 域：把编辑器草稿写回配置文档的 `shell:` 段。
 *
 * 空值（强制路径、探针超时、候选链为空）一律**删除键**而不是写空串，这样
 * runtime.yaml 里不会留下 `path: ""` 这种把「未设置」变成「显式空值」的噪音。
 */
export function createShellDomain(core: ConfigEditorCore) {
  const { setDraftParsed } = core;

  function handleShellConfigChange(next: RuntimeShellConfigSummary) {
    setDraftParsed((current: unknown) => {
      const currentShellValue = getConfigValueAtPath(current, ["shell"]);
      const currentShell = isConfigRecord(currentShellValue)
        ? { ...currentShellValue }
        : {};

      currentShell.autoDetect = next.autoDetect;
      currentShell.strictProbe = next.strictProbe;

      const prefer = next.prefer.filter((item) => item.trim().length > 0);
      if (prefer.length > 0) {
        currentShell.prefer = prefer;
      } else {
        delete currentShell.prefer;
      }

      const path = next.path.trim();
      if (path) {
        currentShell.path = path;
      } else {
        delete currentShell.path;
      }

      const probeTimeout = next.probeTimeout.trim();
      if (probeTimeout) {
        currentShell.probeTimeout = probeTimeout;
      } else {
        delete currentShell.probeTimeout;
      }

      const scriptDelivery = next.scriptDelivery.trim();
      if (scriptDelivery) {
        currentShell.scriptDelivery = scriptDelivery;
      } else {
        delete currentShell.scriptDelivery;
      }

      if (Object.keys(next.env).length > 0) {
        currentShell.env = next.env;
      } else {
        delete currentShell.env;
      }

      return setConfigValueAtPath(current, ["shell"], currentShell);
    });
  }

  return { handleShellConfigChange };
}

export type ShellDomain = ReturnType<typeof createShellDomain>;
