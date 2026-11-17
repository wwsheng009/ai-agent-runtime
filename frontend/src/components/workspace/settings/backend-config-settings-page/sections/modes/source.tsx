// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
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
      <div className="rounded-panel border border-border bg-surface-softer p-3">
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
        </div>
      </div>

      <div className="rounded-panel border border-border bg-surface-softer p-3">
        <Textarea
          size="md"
          surface="soft"
          className="min-h-[44rem] font-mono leading-6"
          spellCheck={false}
          value={draftRaw}
          onChange={(event) => setDraftRaw(event.target.value)}
        />
      </div>
    </>
  );
}
