import { WifiIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsInlineToggleCard } from "./settings-inline-toggle-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { type RuntimeWebsocketConfigSummary } from "./runtime-websocket-domain-utils";

type RuntimeWebsocketDomainEditorProps = {
  config: RuntimeWebsocketConfigSummary;
  onChange: (next: RuntimeWebsocketConfigSummary) => void;
};

export function RuntimeWebsocketDomainEditor({
  config,
  onChange,
}: RuntimeWebsocketDomainEditorProps) {
  function update(patch: Partial<RuntimeWebsocketConfigSummary>) {
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
              <WifiIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-[var(--foreground)]">
                WebSocket 配置
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                维护 Responses WS passthrough、HTTP bridge 和 Realtime WS 的核心配置。
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <Badge>{config.enabled ? "websocket 开" : "websocket 关"}</Badge>
            <Badge>{config.responsesIngressEnabled ? "responses 开" : "responses 关"}</Badge>
            <Badge>{config.realtimeIngressEnabled ? "realtime 开" : "realtime 关"}</Badge>
          </SettingsBadgeList>
        </div>
      </div>

      <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <div className="text-[13px] font-semibold text-[var(--foreground)]">总开关</div>
            <div className="mt-1 text-xs text-[var(--muted-foreground)]">
              控制 websocket 模块是否整体启用。
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
            <input
              type="checkbox"
              className="h-4 w-4 accent-[var(--accent-primary)]"
              checked={config.enabled}
              onChange={(event) => update({ enabled: event.target.checked })}
            />
            启用
          </label>
        </div>
      </div>

      <div className="grid gap-3 xl:grid-cols-2">
        <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-[var(--foreground)]">Responses WS</div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                管理 `/v1/responses` passthrough、HTTP bridge 和连接池行为。
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.responsesIngressEnabled}
                onChange={(event) =>
                  update({ responsesIngressEnabled: event.target.checked })
                }
              />
              启用 ingress
            </label>
          </div>

          <div className="grid gap-3">
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="responses.capacity.max_active_connections">
                <input
                  className={editorControlClassName}
                  value={config.responsesMaxActiveConnections}
                  onChange={(event) =>
                    update({
                      responsesMaxActiveConnections: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="responses.affinity_ttl">
                <input
                  className={editorControlClassName}
                  value={config.responsesAffinityTtl}
                  onChange={(event) =>
                    update({ responsesAffinityTtl: event.target.value })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="responses.handshake_max_retries">
                <input
                  className={editorControlClassName}
                  value={config.responsesHandshakeMaxRetries}
                  onChange={(event) =>
                    update({
                      responsesHandshakeMaxRetries: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>

            <ConfigFormField
              label="responses.compat_bridge_source_protocols"
              description="支持换行或逗号输入多个源协议。"
            >
              <textarea
                className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                value={config.responsesCompatBridgeSourceProtocolsText}
                onChange={(event) =>
                  update({
                    responsesCompatBridgeSourceProtocolsText: event.target.value,
                  })
                }
              />
            </ConfigFormField>

            <div className="grid gap-3 xl:grid-cols-2">
              <ToggleCard
                label="responses.http_bridge_enabled"
                checked={config.responsesHttpBridgeEnabled}
                onChange={(checked) =>
                  update({ responsesHttpBridgeEnabled: checked })
                }
              />
              <ToggleCard
                label="responses.compat_bridge_enabled"
                checked={config.responsesCompatBridgeEnabled}
                onChange={(checked) =>
                  update({ responsesCompatBridgeEnabled: checked })
                }
              />
              <ToggleCard
                label="responses.allow_passthrough_only"
                checked={config.responsesAllowPassthroughOnly}
                onChange={(checked) =>
                  update({ responsesAllowPassthroughOnly: checked })
                }
              />
              <ToggleCard
                label="responses.metrics.enabled"
                checked={config.responsesMetricsEnabled}
                onChange={(checked) =>
                  update({ responsesMetricsEnabled: checked })
                }
              />
              <ToggleCard
                label="responses.metrics.close_code_labels_enabled"
                checked={config.responsesCloseCodeLabelsEnabled}
                onChange={(checked) =>
                  update({ responsesCloseCodeLabelsEnabled: checked })
                }
              />
              <ToggleCard
                label="responses.connection_pooling_enabled"
                checked={config.responsesConnectionPoolingEnabled}
                onChange={(checked) =>
                  update({ responsesConnectionPoolingEnabled: checked })
                }
              />
              <ToggleCard
                label="responses.pre_first_event_retry_once"
                checked={config.responsesPreFirstEventRetryOnce}
                onChange={(checked) =>
                  update({ responsesPreFirstEventRetryOnce: checked })
                }
              />
              <ToggleCard
                label="responses.failover_on_handshake_error"
                checked={config.responsesFailoverOnHandshakeError}
                onChange={(checked) =>
                  update({ responsesFailoverOnHandshakeError: checked })
                }
              />
            </div>
          </div>
        </div>

        <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-[var(--foreground)]">Realtime WS</div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                管理 `/v1/realtime` ingress、容量和握手失败处理。
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.realtimeIngressEnabled}
                onChange={(event) =>
                  update({ realtimeIngressEnabled: event.target.checked })
                }
              />
              启用 ingress
            </label>
          </div>

          <div className="grid gap-3">
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="realtime.capacity.max_active_connections">
                <input
                  className={editorControlClassName}
                  value={config.realtimeMaxActiveConnections}
                  onChange={(event) =>
                    update({
                      realtimeMaxActiveConnections: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="realtime.handshake_max_retries">
                <input
                  className={editorControlClassName}
                  value={config.realtimeHandshakeMaxRetries}
                  onChange={(event) =>
                    update({
                      realtimeHandshakeMaxRetries: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ToggleCard
                label="realtime.metrics.enabled"
                checked={config.realtimeMetricsEnabled}
                onChange={(checked) =>
                  update({ realtimeMetricsEnabled: checked })
                }
              />
              <ToggleCard
                label="realtime.metrics.close_code_labels_enabled"
                checked={config.realtimeCloseCodeLabelsEnabled}
                onChange={(checked) =>
                  update({ realtimeCloseCodeLabelsEnabled: checked })
                }
              />
              <ToggleCard
                label="realtime.failover_on_handshake_error"
                checked={config.realtimeFailoverOnHandshakeError}
                onChange={(checked) =>
                  update({ realtimeFailoverOnHandshakeError: checked })
                }
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function ToggleCard({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (next: boolean) => void;
}) {
  return (
    <SettingsInlineToggleCard
      checked={checked}
      label={label}
      onCheckedChange={onChange}
    />
  );
}
