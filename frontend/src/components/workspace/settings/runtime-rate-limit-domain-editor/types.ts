import {
  type RuntimeRateLimitApiKeyLimitSummary,
  type RuntimeRateLimitConfigSummary,
  type RuntimeRateLimitPathLimitSummary,
} from "../runtime-rate-limit-domain-utils";
import {
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "../runtime-rate-limit-domain-form-utils";

export type RuntimeRateLimitDomainEditorProps = {
  apiKeyLimits: RuntimeRateLimitApiKeyLimitSummary[];
  onChangeConfig: (next: RuntimeRateLimitConfigSummary) => void;
  onDeleteApiKeyLimit: (index: number) => void;
  onDeletePathLimit: (path: string) => void;
  onSaveApiKeyLimit: (
    draft: RateLimitApiKeyDraftInput,
    editingIndex: number | null,
  ) => string | null;
  onSavePathLimit: (
    draft: RateLimitPathDraftInput,
    previousPath: string | null,
  ) => string | null;
  pathLimits: RuntimeRateLimitPathLimitSummary[];
  rateLimitConfig: RuntimeRateLimitConfigSummary;
};

