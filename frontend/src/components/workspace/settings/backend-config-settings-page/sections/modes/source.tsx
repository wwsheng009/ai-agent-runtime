// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { InfoIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { editorControlClassName } from "../../../editor-control-class";
import { SummaryPill } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function SourceModeSection({ core }: { core: ConfigEditorCore }) {
  const {
    draftLineCount,
    draftRaw,
    setDraftRaw,
    t,
  } = core;

  return (
    <>
      <div className="rounded-[0.9rem] border border-border bg-surface-softer p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-base font-semibold text-foreground">
              {t("editor.source.title")}
            </div>
            <SummaryPill
              label={t("editor.source.lines")}
              value={`${draftLineCount}`}
            />
            <SummaryPill
              label={t("editor.source.chars")}
              value={`${draftRaw.length}`}
            />
            <Badge>{t("editor.source.preserveComments")}</Badge>
          </div>
          <details className="rounded-[0.75rem] border border-border bg-surface-solid px-2.5 py-1.5">
            <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-muted-foreground">
              <InfoIcon
                size={14}
                className="text-accent-primary"
              />
              {t("editor.source.helpTitle")}
            </summary>
            <div className="mt-2.5 max-w-[30rem] text-sm leading-6 text-muted-foreground">
              {t("editor.source.helpBody")}
            </div>
          </details>
        </div>
      </div>

      <div className="rounded-[0.9rem] border border-border bg-surface-softer p-3">
        <textarea
          className={cn(
            editorControlClassName,
            "min-h-[44rem] resize-y font-mono app-text-13 leading-6",
          )}
          spellCheck={false}
          value={draftRaw}
          onChange={(event) => setDraftRaw(event.target.value)}
        />
      </div>
    </>
  );
}
