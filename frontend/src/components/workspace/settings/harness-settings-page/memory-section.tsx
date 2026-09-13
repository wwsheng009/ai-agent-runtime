// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { BrainIcon } from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type RuntimeHarnessMemoryNote } from "@/lib/runtime-api";

import { editorControlClassName } from "../editor-control-class";
import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsSection } from "../settings-section";

export function HarnessMemorySection({
  actionPending,
  handleAppendMemory,
  loading,
  memoryNotes,
  memoryQuery,
  memoryTags,
  memoryText,
  searchMemory,
  setMemoryQuery,
  setMemoryTags,
  setMemoryText,
  t,
  workspacePath,
}: {
  actionPending: boolean;
  handleAppendMemory: () => Promise<void>;
  loading: boolean;
  memoryNotes: RuntimeHarnessMemoryNote[];
  memoryQuery: string;
  memoryTags: string;
  memoryText: string;
  searchMemory: (query: string) => Promise<void>;
  setMemoryQuery: Dispatch<SetStateAction<string>>;
  setMemoryTags: Dispatch<SetStateAction<string>>;
  setMemoryText: Dispatch<SetStateAction<string>>;
  t: TFunction<"settings">;
  workspacePath: string;
}) {
  return (
      <SettingsSection
        title={t("harness.memoryTitle")}
        description={t("harness.memoryDescription")}
      >
        <div className="space-y-3">
          <SettingsPanelCard
            title={t("harness.memorySearch")}
            icon={
              <BrainIcon size={16} className="text-accent-primary" />
            }
          >
            <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
              <input
                className={editorControlClassName}
                value={memoryQuery}
                onChange={(event) => setMemoryQuery(event.target.value)}
                placeholder={t("harness.memorySearchPlaceholder")}
                aria-label={t("harness.memorySearchPlaceholder")}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    void searchMemory(memoryQuery);
                  }
                }}
              />
              <Button
                variant="secondary"
                size="sm"
                disabled={!workspacePath || loading || actionPending}
                onClick={() => {
                  void searchMemory(memoryQuery);
                }}
              >
                {t("harness.search")}
              </Button>
            </div>
          </SettingsPanelCard>

          <SettingsPanelCard title={t("harness.memoryAppend")}>
            <textarea
              className={`${editorControlClassName} min-h-24`}
              value={memoryText}
              onChange={(event) => setMemoryText(event.target.value)}
              placeholder={t("harness.memoryTextPlaceholder")}
              aria-label={t("harness.memoryTextPlaceholder")}
            />
            <div className="mt-3 grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
              <input
                className={editorControlClassName}
                value={memoryTags}
                onChange={(event) => setMemoryTags(event.target.value)}
                placeholder={t("harness.memoryTagsPlaceholder")}
                aria-label={t("harness.memoryTagsPlaceholder")}
              />
              <Button
                size="sm"
                disabled={
                  !workspacePath ||
                  actionPending ||
                  !memoryText.trim()
                }
                onClick={() => {
                  void handleAppendMemory();
                }}
              >
                {t("harness.append")}
              </Button>
            </div>
          </SettingsPanelCard>

          {memoryNotes.length > 0 ? (
            <div className="space-y-2">
              {memoryNotes.map((note) => (
                <div
                  key={note.id}
                  className="rounded-card-lg border border-border bg-surface-softer px-3 py-2.5"
                >
                  <p className="text-sm leading-6 text-foreground">
                    {note.text}
                  </p>
                  <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    {note.source ? <Badge>{note.source}</Badge> : null}
                    {note.created_at ? <span>{note.created_at}</span> : null}
                    {(note.tags ?? []).map((tag) => (
                      <Badge key={`${note.id}-${tag}`}>#{tag}</Badge>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <SettingsEmptyState variant="dashed">
              {memoryQuery.trim()
                ? t("harness.noMemoryHits")
                : t("harness.noMemoryNotes")}
            </SettingsEmptyState>
          )}
        </div>
      </SettingsSection>
  );
}
