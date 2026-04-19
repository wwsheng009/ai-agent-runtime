import { RouteIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import {
  hasRuntimeProxyConfig,
  summarizeRuntimeProxyConfig,
  type RuntimeProxyConfigSummary,
} from "./runtime-proxy-domain-utils";

type RuntimeProxyDomainEditorProps = {
  config: RuntimeProxyConfigSummary;
  onChange: (next: RuntimeProxyConfigSummary) => void;
};

export function RuntimeProxyDomainEditor({
  config,
  onChange,
}: RuntimeProxyDomainEditorProps) {
  function update(patch: Partial<RuntimeProxyConfigSummary>) {
    onChange({
      ...config,
      ...patch,
    });
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <RouteIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-[var(--foreground)]">
                网络代理配置
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                配置 runtime 上游 HTTP/HTTPS/SOCKS5 代理。留空并关闭后，会回退到进程环境变量或直连。
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <Badge>{config.enabled ? "代理开" : "代理关"}</Badge>
            <Badge>{summarizeRuntimeProxyConfig(config)}</Badge>
            <Badge>
              {hasRuntimeProxyConfig(config) ? "显式配置" : "环境变量回退"}
            </Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <SettingsInlineToggleCard
        checked={config.enabled}
        label="providers.proxy.enabled"
        description="总开关。启用后会按下方 URL 走显式代理；关闭且字段留空时，回退到进程环境变量。"
        onCheckedChange={(checked) => update({ enabled: checked })}
      />

      <div className="grid gap-3 xl:grid-cols-2">
        <ConfigFormField
          label="providers.proxy.http"
          description="HTTP 请求代理地址，也可填写 socks5://host:port。"
        >
          <input
            className={editorControlClassName}
            value={config.http}
            onChange={(event) => update({ http: event.target.value })}
            placeholder="http://127.0.0.1:10810 或 socks5://127.0.0.1:10810"
          />
        </ConfigFormField>

        <ConfigFormField
          label="providers.proxy.https"
          description="HTTPS 请求代理地址，未填写时会回退使用 HTTP 代理。"
        >
          <input
            className={editorControlClassName}
            value={config.https}
            onChange={(event) => update({ https: event.target.value })}
            placeholder="http://127.0.0.1:10810 或 socks5://127.0.0.1:10810"
          />
        </ConfigFormField>
      </div>

      <ConfigFormField
        label="providers.proxy.no_proxy"
        description="逗号分隔的绕过列表，例如 localhost,127.0.0.1,.internal.example.com。"
      >
        <textarea
          className={`${editorControlClassName} min-h-28 resize-y font-mono`}
          value={config.noProxy}
          onChange={(event) => update({ noProxy: event.target.value })}
          placeholder="localhost,127.0.0.1,.internal.example.com"
        />
      </ConfigFormField>
    </div>
  );
}
