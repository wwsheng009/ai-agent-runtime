// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type ProviderQueueProviderDraftInput } from "../runtime-provider-queue-domain-form-utils";
import {
  type RuntimeProviderQueueConfigSummary,
  type RuntimeProviderQueueProviderSummary,
} from "../runtime-provider-queue-domain-utils";

export type RuntimeProviderQueueDomainEditorProps = {
  config: RuntimeProviderQueueConfigSummary;
  onChangeConfig: (next: RuntimeProviderQueueConfigSummary) => void;
  onDeleteProvider: (provider: string) => void;
  onSaveProvider: (
    draft: ProviderQueueProviderDraftInput,
    previousProvider: string | null,
  ) => string | null;
  providers: RuntimeProviderQueueProviderSummary[];
};
