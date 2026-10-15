// P3：自动化任务（Schedule）人工创建表单（kind 选择器 + 目标/计划/时间字段）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { scheduleKindLabelKey } from "@/components/workspace/schedule-panel-shared";
import type { RuntimeScheduleKind, RuntimeScheduleTargetKind } from "@/types/runtime";

import { createKinds } from "./form-state";
import type { CreateFormState } from "./form-state";
import { WeekdayPicker } from "./weekday-picker";

export type ScheduleCreateFormProps = {
  createForm: CreateFormState;
  creating: boolean;
  setCreateForm: Dispatch<SetStateAction<CreateFormState | null>>;
  submitCreate: () => void;
};

export function ScheduleCreateForm({
  createForm,
  creating,
  setCreateForm,
  submitCreate,
}: ScheduleCreateFormProps) {
  const { t } = useTranslation("workspace");
  return (
    <section className="rounded-panel border border-border p-3">
      <h3 className="mb-2 text-xs font-semibold uppercase text-muted-foreground">
        {t("panels.schedule.createTitle")}
      </h3>
      <div className="grid gap-2 sm:grid-cols-2">
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.sessionLabel")}</span>
          <input
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) =>
              setCreateForm({ ...createForm, sessionId: event.target.value })
            }
            placeholder={t("panels.schedule.sessionPlaceholder")}
            value={createForm.sessionId}
          />
        </label>
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.targetLabel")}</span>
          <select
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) =>
              setCreateForm({
                ...createForm,
                targetKind: event.target.value as RuntimeScheduleTargetKind,
              })
            }
            value={createForm.targetKind}
          >
            <option value="session">{t("panels.schedule.target.session")}</option>
            <option value="headless">{t("panels.schedule.target.headless")}</option>
            <option value="workspace">{t("panels.schedule.target.workspace")}</option>
          </select>
        </label>
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.goalLabel")}</span>
          <input
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) => setCreateForm({ ...createForm, goal: event.target.value })}
            value={createForm.goal}
          />
        </label>
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.planLabel")}</span>
          <textarea
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) => setCreateForm({ ...createForm, plan: event.target.value })}
            placeholder={t("panels.schedule.planPlaceholder")}
            rows={3}
            value={createForm.plan}
          />
        </label>
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.titleLabel")}</span>
          <input
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) =>
              setCreateForm({ ...createForm, title: event.target.value })
            }
            value={createForm.title}
          />
        </label>
      </div>
      <label className="mt-2 grid gap-1 text-xs">
        <span className="text-muted-foreground">{t("panels.schedule.promptLabel")}</span>
        <textarea
          className="rounded border border-border bg-transparent px-2 py-1 text-xs"
          onChange={(event) =>
            setCreateForm({ ...createForm, prompt: event.target.value })
          }
          rows={2}
          value={createForm.prompt}
        />
      </label>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <label className="grid gap-1 text-xs">
          <span className="text-muted-foreground">{t("panels.schedule.kindLabel")}</span>
          <select
            className="rounded border border-border bg-transparent px-2 py-1"
            onChange={(event) =>
              setCreateForm({
                ...createForm,
                kind: event.target.value as RuntimeScheduleKind,
              })
            }
            value={createForm.kind}
          >
            {createKinds.map((kind) => (
              <option key={kind} value={kind}>
                {t(scheduleKindLabelKey(kind))}
              </option>
            ))}
          </select>
        </label>
        {(createForm.kind === "after" || createForm.kind === "every") && (
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">
              {t(
                createForm.kind === "after"
                  ? "panels.schedule.minutesAfter"
                  : "panels.schedule.minutesEvery",
              )}
            </span>
            <input
              className="w-24 rounded border border-border bg-transparent px-2 py-1"
              inputMode="numeric"
              onChange={(event) =>
                setCreateForm({ ...createForm, amount: event.target.value })
              }
              value={createForm.amount}
            />
          </label>
        )}
        {createForm.kind === "at" && (
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">{t("panels.schedule.atLabel")}</span>
            <input
              className="rounded border border-border bg-transparent px-2 py-1"
              onChange={(event) =>
                setCreateForm({ ...createForm, time: event.target.value })
              }
              type="datetime-local"
              value={createForm.time}
            />
          </label>
        )}
        {(createForm.kind === "daily" || createForm.kind === "weekly" || createForm.kind === "cron") && (
          <>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">{t("panels.schedule.timeLabel")}</span>
              <input
                className="w-28 rounded border border-border bg-transparent px-2 py-1"
                onChange={(event) =>
                  setCreateForm({ ...createForm, time: event.target.value })
                }
                placeholder={t("panels.schedule.timePlaceholder")}
                value={createForm.time}
              />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">{t("panels.schedule.zoneLabel")}</span>
              <input
                className="rounded border border-border bg-transparent px-2 py-1"
                onChange={(event) =>
                  setCreateForm({ ...createForm, timeZone: event.target.value })
                }
                placeholder={t("panels.schedule.zonePlaceholder")}
                value={createForm.timeZone}
              />
            </label>
          </>
        )}
        {createForm.kind === "weekly" && (
          <WeekdayPicker
            onToggle={(day) =>
              setCreateForm((form) =>
                form
                  ? {
                      ...form,
                      weekdays: form.weekdays.includes(day)
                        ? form.weekdays.filter((d) => d !== day)
                        : [...form.weekdays, day],
                    }
                  : form,
              )
            }
            selected={createForm.weekdays}
          />
        )}
        {createForm.kind === "cron" && (
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">{t("panels.schedule.cronLabel")}</span>
            <input
              className="rounded border border-border bg-transparent px-2 py-1"
              onChange={(event) =>
                setCreateForm({ ...createForm, cron: event.target.value })
              }
              placeholder="*/5 * * * *"
              value={createForm.cron}
            />
          </label>
        )}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Button disabled={creating} onClick={() => void submitCreate()} size="sm">
          {t("panels.schedule.createSubmit")}
        </Button>
        <Button
          onClick={() => setCreateForm(null)}
          size="sm"
          variant="ghost"
        >
          {t("panels.schedule.cancel")}
        </Button>
      </div>
    </section>
  );
}
