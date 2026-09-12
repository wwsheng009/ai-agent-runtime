// 由 components/workspace/settings/runtime-routing-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeRoutingConfigSummary,
  type RuntimeRouteSummary,
} from "../runtime-routing-domain-utils";
import { type RouteDraftInput } from "../runtime-routing-domain-form-utils";

export type RuntimeRoutingDomainEditorProps = {
  availableGroups: string[];
  onChangeConfig: (next: RuntimeRoutingConfigSummary) => void;
  onDeleteRoute: (index: number) => void;
  onMoveRoute: (index: number, direction: "up" | "down") => void;
  onSaveRoute: (
    draft: RouteDraftInput,
    editingIndex: number | null,
  ) => string | null;
  routeConfig: RuntimeRoutingConfigSummary;
  routes: RuntimeRouteSummary[];
};
