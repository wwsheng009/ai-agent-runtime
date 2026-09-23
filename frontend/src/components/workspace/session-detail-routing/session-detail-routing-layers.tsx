// 「路由」区块的写入层选择器 + 写入目标回显（§5.4）。
//
// 语义纪律：
//   * 可写性只认后端 `panel.writable_layers`——未开放的层置灰并标注，
//     不给「点了必然失败」的入口；
//   * `target_path` 优先用本次 PATCH 的服务端回显，其次回落面板里后端给出的
//     层路径；session 层没有文件（覆盖随会话记录持久化），用文案说明。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { SESSION_DETAIL_SECTION_LABEL_CLASS } from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";
import type {
  RoutingPanelMetadata,
  SessionRoutingTargetLayer,
} from "@/types/runtime";
import { SESSION_ROUTING_TARGET_LAYERS } from "@/types/runtime";

import { isRoutingLayerWritable } from "./session-detail-routing-shared";

const LAYER_LABEL_KEY = {
  session: "panels.sessionDetail.routing.layers.session",
  workspace: "panels.sessionDetail.routing.layers.workspace",
  config: "panels.sessionDetail.routing.layers.config",
} as const;

export type SessionDetailRoutingLayersProps = {
  panel: RoutingPanelMetadata | null;
  layer: SessionRoutingTargetLayer;
  /** 已解析的写入目标（见 resolveRoutingTargetPath）。 */
  targetPath: string;
  saving: boolean;
  onLayerChange: (layer: SessionRoutingTargetLayer) => void;
};

export function SessionDetailRoutingLayers({
  panel,
  layer,
  targetPath,
  saving,
  onLayerChange,
}: SessionDetailRoutingLayersProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="grid gap-1">
      <div
        aria-label={t("panels.sessionDetail.routing.layers.label")}
        className="flex flex-wrap gap-1"
        role="group"
      >
        {SESSION_ROUTING_TARGET_LAYERS.map((candidate) => {
          const writable = isRoutingLayerWritable(panel, candidate);
          return (
            <Button
              aria-pressed={layer === candidate}
              className={cn(
                "h-6 px-2 app-text-11",
                layer === candidate && "border-accent-primary/40 text-accent-primary",
              )}
              data-testid={`routing-layer-${candidate}`}
              disabled={!writable || saving}
              key={candidate}
              onClick={() => onLayerChange(candidate)}
              size="sm"
              title={
                writable
                  ? undefined
                  : t("panels.sessionDetail.routing.layers.lockedHint")
              }
              type="button"
              variant="ghost"
            >
              {t(LAYER_LABEL_KEY[candidate])}
              {writable ? null : (
                <span className="app-text-10 text-muted-foreground">
                  {t("panels.sessionDetail.routing.layers.locked")}
                </span>
              )}
            </Button>
          );
        })}
      </div>
      {panel?.childSession ? (
        <p
          className="app-text-10 leading-4 text-muted-foreground"
          data-testid="routing-child-session-hint"
        >
          {t("panels.sessionDetail.routing.layers.childSessionHint")}
        </p>
      ) : null}
      <div className="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-1.5">
        <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
          {t("panels.sessionDetail.routing.target.label")}
        </span>
        <span
          className="min-w-0 break-all app-text-11 text-muted-foreground"
          data-testid="routing-target-path"
        >
          {targetPath ||
            (layer === "session"
              ? t("panels.sessionDetail.routing.target.session")
              : t("panels.sessionDetail.routing.target.none"))}
        </span>
      </div>
    </div>
  );
}
