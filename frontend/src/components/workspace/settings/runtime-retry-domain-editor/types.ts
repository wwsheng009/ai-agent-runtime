// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeRetryConfigSummary,
  type RuntimeRetryRuleSummary,
} from "../runtime-retry-domain-utils";

export type RetryRuleDraftInput = {
  backoffMultiplier: string;
  description: string;
  enabled: boolean;
  errorCodeCodesText: string;
  errorCodePattern: string;
  keywordCaseSensitive: boolean;
  keywordPatternsText: string;
  keywordValuesText: string;
  maxRetries: string;
  name: string;
  retryDelayMs: string;
  statusCodeRange: string;
};

export type RuntimeRetryDomainEditorProps = {
  config: RuntimeRetryConfigSummary;
  onChangeConfig: (next: RuntimeRetryConfigSummary) => void;
  onDeleteRule: (index: number) => void;
  onMoveRule: (index: number, direction: "up" | "down") => void;
  onSaveRule: (
    draft: RetryRuleDraftInput,
    editingIndex: number | null,
  ) => string | null;
  rules: RuntimeRetryRuleSummary[];
};
