import { type TFunction } from "i18next";
import { useState } from "react";

import { getMemoryWhyNot, RuntimeApiError, type RuntimeMemoryWhyNot } from "@/lib/runtime-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

function errorMessage(error: unknown) {
  return error instanceof Error && error.message.trim()
    ? error.message
    : String(error);
}

export function MemoryV2WhyNot({
  t,
  workspacePath,
  disabled,
  onError,
}: {
  t: TFunction<"settings">;
  workspacePath: string;
  disabled: boolean;
  onError: (message: string | null) => void;
}) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<RuntimeMemoryWhyNot | null>(null);
  const [loading, setLoading] = useState(false);

  async function explain() {
    const trimmed = query.trim();
    if (!trimmed || !workspacePath) return;
    setLoading(true);
    onError(null);
    try {
      setResult(await getMemoryWhyNot(workspacePath, { q: trimmed, limit: 8 }));
    } catch (error) {
      if (error instanceof RuntimeApiError && error.status === 403) {
        onError(t("harness.memoryWhyNotForbidden"));
      } else {
        onError(errorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="mt-3 rounded-card border border-border px-3 py-2.5">
      <div className="text-xs font-medium text-foreground">{t("harness.memoryWhyNotTitle")}</div>
      <div className="mt-2 flex flex-wrap gap-2">
        <Input
          size="sm"
          aria-label={t("harness.memoryWhyNotPlaceholder")}
          className="min-w-0 flex-1"
          placeholder={t("harness.memoryWhyNotPlaceholder")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void explain();
          }}
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={disabled || loading || !query.trim()}
          onClick={() => void explain()}
        >
          {t("harness.memoryWhyNotRun")}
        </Button>
      </div>
      {result ? (
        <div className="mt-2 space-y-2 text-xs text-muted-foreground">
          <div>
            {t("harness.memoryWhyNotSummary", {
              candidates: String(result.candidate_count),
              filtered: String(result.filtered_count),
              selected: String(result.selected.length),
              tokens: String(result.token_estimate),
              budget: String(result.token_budget),
            })}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {result.selected.map((item) => (
              <Badge key={`selected-${item.stage}-${item.id}`}>
                {item.stage}:{item.id}
              </Badge>
            ))}
          </div>
          {result.dropped.length > 0 ? (
            <div className="space-y-1">
              <div className="font-medium text-foreground">{t("harness.memoryWhyNotDropped")}</div>
              {result.dropped.slice(0, 12).map((item) => (
                <div key={`dropped-${item.id}-${item.reason}`}>
                  {item.id}: {item.reason}
                </div>
              ))}
            </div>
          ) : (
            <div>{t("harness.memoryWhyNotNoDrops")}</div>
          )}
        </div>
      ) : null}
    </div>
  );
}
