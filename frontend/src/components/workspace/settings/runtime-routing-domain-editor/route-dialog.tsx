// 由 components/workspace/settings/runtime-routing-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import { Select } from "@/components/ui/select";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type RouteDraftInput } from "../runtime-routing-domain-form-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

const routeMatchTypeOptions = [
  { value: "prefix", label: "prefix" },
  { value: "exact", label: "exact" },
  { value: "regex", label: "regex" },
] as const;

export function RuntimeRoutingRouteDialog({
  availableGroups,
  dialogError,
  dialogOpen,
  draft,
  editingIndex,
  handleSave,
  setDialogOpen,
  setDraft,
  t,
}: {
  availableGroups: string[];
  dialogError: string | null;
  dialogOpen: boolean;
  draft: RouteDraftInput;
  editingIndex: number | null;
  handleSave: () => void;
  setDialogOpen: (open: boolean) => void;
  setDraft: Dispatch<SetStateAction<RouteDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? t("editor.routing.dialog.createTitle")
            : t("editor.routing.dialog.editTitle", {
                index: String(editingIndex + 1),
              })
        }
        description={t("editor.routing.dialog.description")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note={t("editor.routing.dialog.footerNote")}
            confirmLabel={t("editor.routing.actions.saveRoute")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="match_path"
              description={t("editor.routing.fields.matchPathHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.matchPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, matchPath: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="match_type"
              description={t("editor.routing.fields.matchTypeHelp")}
            >
              <Select
                ariaLabel={t("editor.routing.fields.matchTypeAria")}
                value={draft.matchType}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, matchType: value }))
                }
                options={routeMatchTypeOptions}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField
              label="group"
              description={t("editor.routing.fields.groupHelp")}
            >
              <div className="space-y-2">
                <Select
                  ariaLabel={t("editor.routing.fields.groupAria")}
                  value={draft.group}
                  onChange={(value) =>
                    setDraft((current) => ({ ...current, group: value }))
                  }
                  options={[
                    { value: "", label: t("editor.routing.fields.groupPlaceholder") },
                    ...availableGroups.map((groupName) => ({
                      value: groupName,
                      label: groupName,
                    })),
                  ]}
                  className="w-full"
                  triggerClassName={editorControlClassName}
                  optionClassName="text-sm"
                />
                <input
                  className={editorControlClassName}
                  value={draft.group}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, group: event.target.value }))
                  }
                  placeholder={t("editor.routing.fields.groupInputPlaceholder")}
                />
              </div>
            </ConfigFormField>
            <ConfigFormField
              label="protocol"
              description={t("editor.routing.fields.protocolHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.protocol}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, protocol: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="pipeline"
              description={t("editor.routing.fields.pipelineHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.pipeline}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, pipeline: event.target.value }))
                }
                placeholder="chat-completions"
              />
            </ConfigFormField>
            <ConfigFormField label="priority">
              <input
                className={editorControlClassName}
                value={draft.priority}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, priority: event.target.value }))
                }
                placeholder="10"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="match_models"
              description={t("editor.routing.fields.matchModelsHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.matchModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    matchModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="exclude_models"
              description={t("editor.routing.fields.excludeModelsHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.excludeModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    excludeModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="match_model_regexes"
              description={t("editor.routing.fields.matchModelRegexesHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.matchModelRegexesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    matchModelRegexesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="exclude_model_regexes"
              description={t("editor.routing.fields.excludeModelRegexesHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.excludeModelRegexesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    excludeModelRegexesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField
            label={t("editor.routing.fields.extraJson")}
            description={t("editor.routing.fields.extraJsonHelp")}
          >
            <textarea
              className={`${editorControlClassName} min-h-40 resize-y font-mono`}
              value={draft.extraJson}
              onChange={(event) =>
                setDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>
        </div>
      </ConfigDomainDialog>
  );
}
