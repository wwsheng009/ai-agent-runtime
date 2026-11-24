import { PlusIcon, Trash2Icon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createRuntimeTeamRunDraft,
  isRuntimeTeamProductCapabilitySupported,
  type RuntimeTeamProductCapabilities,
  type RuntimeTeamRun,
} from "@/lib/runtime-api";

type StudioMember = {
  id: string;
  name: string;
  role: string;
  profile: string;
  capabilities: string;
  session_id: string;
  state: string;
};

type StudioTask = {
  id: string;
  title: string;
  goal: string;
  assignee: string;
  priority: string;
  difficulty: string;
  difficulty_rationale: string;
  task_type: string;
  task_subject: string;
  inputs: string;
  read_paths: string;
  write_paths: string;
  deliverables: string;
};

type StudioDependency = {
  task_id: string;
  depends_on_id: string;
};

type TeamStudioPanelProps = {
  capabilities: RuntimeTeamProductCapabilities;
  onCancel: () => void;
  onCreated: (run: RuntimeTeamRun) => void;
};

const defaultMembers: StudioMember[] = [
  {
    id: "member-1",
    name: "Reviewer",
    role: "reviewer",
    profile: "reviewer",
    capabilities: "",
    session_id: "",
    state: "idle",
  },
  {
    id: "member-2",
    name: "Implementer",
    role: "implementer",
    profile: "implementer",
    capabilities: "",
    session_id: "",
    state: "idle",
  },
];

const defaultTasks: StudioTask[] = [
  {
    id: "task-1",
    title: "Review the scope",
    goal: "Review the requested change and report constraints.",
    assignee: "member-1",
    priority: "0",
    difficulty: "normal",
    difficulty_rationale: "",
    task_type: "understand",
    task_subject: "scope review",
    inputs: "",
    read_paths: "",
    write_paths: "",
    deliverables: "",
  },
  {
    id: "task-2",
    title: "Implement the change",
    goal: "Implement the change after the review is complete.",
    assignee: "member-2",
    priority: "0",
    difficulty: "normal",
    difficulty_rationale: "",
    task_type: "implement",
    task_subject: "requested change",
    inputs: "",
    read_paths: "",
    write_paths: "",
    deliverables: "",
  },
];

const defaultDependencies: StudioDependency[] = [
  { task_id: "task-2", depends_on_id: "task-1" },
];

const memberStates = ["idle", "busy", "blocked", "offline"] as const;
const taskDifficulties = ["easy", "normal", "hard", "expert"] as const;
const taskTypes = [
  "config",
  "explore",
  "generate",
  "implement",
  "integration",
  "migrate",
  "modify",
  "refactor",
  "security",
  "test",
  "understand",
  "verify",
] as const;

function createIdempotencyKey(): string {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi && typeof cryptoApi.randomUUID === "function") {
    return `web-team-run-draft-${cryptoApi.randomUUID()}`;
  }
  return `web-team-run-draft-${Date.now()}`;
}

function splitList(value: string): string[] {
  return value
    .split(/\r?\n|,/)
    .map((path) => path.trim())
    .filter(Boolean);
}

function updateAt<T>(items: T[], index: number, update: (item: T) => T): T[] {
  return items.map((item, itemIndex) =>
    itemIndex === index ? update(item) : item,
  );
}

function nextId(prefix: string, ids: string[]): string {
  const used = new Set(ids);
  let index = 1;
  while (used.has(`${prefix}-${index}`)) {
    index += 1;
  }
  return `${prefix}-${index}`;
}

export function TeamStudioPanel({
  capabilities,
  onCancel,
  onCreated,
}: TeamStudioPanelProps) {
  const { t } = useTranslation("workspace");
  const [goal, setGoal] = useState("");
  const [members, setMembers] = useState<StudioMember[]>(defaultMembers);
  const [tasks, setTasks] = useState<StudioTask[]>(defaultTasks);
  const [dependencies, setDependencies] =
    useState<StudioDependency[]>(defaultDependencies);
  const [error, setError] = useState<string | null>(null);
  const [isCreating, setIsCreating] = useState(false);
  const idempotencyKeysRef = useRef(new Map<string, string>());
  const requestControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    return () => {
      requestControllerRef.current?.abort();
      requestControllerRef.current = null;
    };
  }, []);

  const canCreate = isRuntimeTeamProductCapabilitySupported(
    capabilities,
    "run_draft_create",
  );

  function validate(): string | null {
    if (!goal.trim()) {
      return t("panels.teamsPanels.studio.validation.goal");
    }
    if (members.length === 0) {
      return t("panels.teamsPanels.studio.validation.members");
    }
    if (tasks.length === 0) {
      return t("panels.teamsPanels.studio.validation.tasks");
    }

    const memberIDs = new Set<string>();
    for (const member of members) {
      const id = member.id.trim();
      if (!id) {
        return t("panels.teamsPanels.studio.validation.memberId");
      }
      if (memberIDs.has(id)) {
        return t("panels.teamsPanels.studio.validation.duplicateMember", {
          id,
        });
      }
      memberIDs.add(id);
    }

    const taskIDs = new Set<string>();
    for (const task of tasks) {
      const id = task.id.trim();
      if (!id) {
        return t("panels.teamsPanels.studio.validation.taskId");
      }
      if (taskIDs.has(id)) {
        return t("panels.teamsPanels.studio.validation.duplicateTask", { id });
      }
      if (!task.title.trim() && !task.goal.trim()) {
        return t("panels.teamsPanels.studio.validation.taskGoal", { id });
      }
      if (!Number.isInteger(Number(task.priority.trim() || "0"))) {
        return t("panels.teamsPanels.studio.validation.taskPriority", { id });
      }
      if (task.assignee.trim() && !memberIDs.has(task.assignee.trim())) {
        return t("panels.teamsPanels.studio.validation.unknownAssignee", {
          id: task.id.trim(),
          assignee: task.assignee.trim(),
        });
      }
      taskIDs.add(id);
    }

    for (const dependency of dependencies) {
      const taskID = dependency.task_id.trim();
      const dependsOnID = dependency.depends_on_id.trim();
      if (!taskID && !dependsOnID) {
        continue;
      }
      if (!taskID || !dependsOnID) {
        return t("panels.teamsPanels.studio.validation.dependencyPair");
      }
      if (taskID === dependsOnID) {
        return t("panels.teamsPanels.studio.validation.selfDependency", {
          id: taskID,
        });
      }
      if (!taskIDs.has(taskID) || !taskIDs.has(dependsOnID)) {
        return t("panels.teamsPanels.studio.validation.unknownDependency");
      }
    }
    return null;
  }

  async function handleCreate() {
    if (!canCreate || isCreating) {
      return;
    }
    const validationError = validate();
    if (validationError) {
      setError(validationError);
      return;
    }

    const normalizedMembers = members.map((member) => ({
      id: member.id.trim(),
      name: member.name.trim(),
      role: member.role.trim(),
      profile: member.profile.trim(),
      capabilities: splitList(member.capabilities),
      session_id: member.session_id.trim(),
      state: member.state.trim() || "idle",
    }));
    const normalizedTasks = tasks.map((task) => ({
      id: task.id.trim(),
      title: task.title.trim(),
      goal: task.goal.trim(),
      assignee: task.assignee.trim(),
      priority: Number(task.priority.trim() || "0"),
      difficulty: task.difficulty.trim(),
      difficulty_rationale: task.difficulty_rationale.trim(),
      task_type: task.task_type.trim(),
      task_subject: task.task_subject.trim(),
      inputs: splitList(task.inputs),
      read_paths: splitList(task.read_paths),
      write_paths: splitList(task.write_paths),
      deliverables: splitList(task.deliverables),
    }));
    const normalizedDependencies = dependencies
      .map((dependency) => ({
        task_id: dependency.task_id.trim(),
        depends_on: dependency.depends_on_id.trim(),
      }))
      .filter(
        (dependency) => dependency.task_id && dependency.depends_on,
      );
    const request = {
      goal: goal.trim(),
      config: {
        strategy: "manual",
        members: normalizedMembers,
        tasks: normalizedTasks,
        dependencies: normalizedDependencies,
      },
    };
    const fingerprint = JSON.stringify(request);

    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setIsCreating(true);
    setError(null);
    let idempotencyKey = idempotencyKeysRef.current.get(fingerprint);
    if (!idempotencyKey) {
      idempotencyKey = createIdempotencyKey();
      idempotencyKeysRef.current.set(fingerprint, idempotencyKey);
    }

    try {
      const response = await createRuntimeTeamRunDraft(
        request,
        {
          idempotencyKey,
          signal: controller.signal,
        },
      );
      if (
        controller.signal.aborted ||
        requestControllerRef.current !== controller
      ) {
        return;
      }
      idempotencyKeysRef.current.clear();
      onCreated(response.run);
    } catch (createError) {
      if (
        controller.signal.aborted ||
        requestControllerRef.current !== controller
      ) {
        return;
      }
      setError(
        createError instanceof Error
          ? createError.message
          : t("panels.teamsPanels.studio.createFailed"),
      );
    } finally {
      if (
        !controller.signal.aborted &&
        requestControllerRef.current === controller
      ) {
        setIsCreating(false);
      }
    }
  }

  return (
    <section
      aria-labelledby="team-studio-title"
      className="mb-3 rounded-panel-lg border border-accent-teal/25 bg-accent-teal/5 p-3.5"
    >
      <div className="flex flex-col gap-3 border-b border-border pb-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <div className="app-text-10 uppercase tracking-[0.16em] text-accent-teal">
            {t("panels.teamsPanels.studio.eyebrow")}
          </div>
          <h3 id="team-studio-title" className="mt-1 text-base font-semibold text-foreground">
            {t("panels.teamsPanels.studio.title")}
          </h3>
          <p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">
            {t("panels.teamsPanels.studio.description")}
          </p>
        </div>
        <button
          type="button"
          onClick={onCancel}
          className="min-h-11 rounded-control border border-border px-3 py-2 text-xs font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
        >
          {t("panels.teamsPanels.studio.close")}
        </button>
      </div>

      {!canCreate ? (
        <p className="mt-3 rounded-control border border-accent-gold/25 bg-accent-gold/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
          {t("panels.teamsPanels.studio.unsupported")}
        </p>
      ) : (
        <>
          <div className="mt-3">
            <label className="block text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              {t("panels.teamsPanels.studio.goal")}
              <input
                value={goal}
                onChange={(event) => setGoal(event.target.value)}
                className="mt-1.5 min-h-11 w-full rounded-control border border-border bg-surface-solid px-3 py-2 text-sm font-normal normal-case tracking-normal text-foreground outline-none transition focus:border-accent-teal/60"
                placeholder={t("panels.teamsPanels.studio.goalPlaceholder")}
              />
            </label>
          </div>

          <div className="mt-4 grid gap-4 xl:grid-cols-2">
            <fieldset className="min-w-0 rounded-card border border-border bg-surface-softer p-3">
              <legend className="px-1 text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                {t("panels.teamsPanels.studio.membersTitle")}
              </legend>
              <p className="mb-3 mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.teamsPanels.studio.membersHint")}
              </p>
              <div className="space-y-3">
                {members.map((member, index) => (
                  <div
                    key={`${member.id}-${index}`}
                    className="rounded-control border border-border bg-surface-solid p-2.5"
                  >
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <span className="text-xs font-semibold text-foreground">
                        {t("panels.teamsPanels.studio.memberLabel", {
                          count: index + 1,
                        })}
                      </span>
                      {members.length > 1 ? (
                        <button
                          type="button"
                          onClick={() =>
                            setMembers((current) =>
                              current.filter((_, itemIndex) => itemIndex !== index),
                            )
                          }
                          className="inline-flex min-h-11 items-center gap-1 rounded-control border border-border px-2 py-1 text-xs text-muted-foreground transition hover:border-accent-red/40 hover:text-accent-red"
                          aria-label={t(
                            "panels.teamsPanels.studio.removeMember",
                            { count: index + 1 },
                          )}
                        >
                          <Trash2Icon size={13} aria-hidden="true" />
                          {t("panels.teamsPanels.studio.remove")}
                        </button>
                      ) : null}
                    </div>
                    <div className="grid gap-2 sm:grid-cols-2">
                      <StudioInput
                        label={t("panels.teamsPanels.studio.id")}
                        value={member.id}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              id: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.name")}
                        value={member.name}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              name: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.role")}
                        value={member.role}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              role: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.profile")}
                        value={member.profile}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              profile: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.capabilities")}
                        value={member.capabilities}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              capabilities: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.sessionId")}
                        value={member.session_id}
                        onChange={(value) =>
                          setMembers((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              session_id: value,
                            })),
                          )
                        }
                      />
                      <label className="block text-xs text-muted-foreground">
                        {t("panels.teamsPanels.studio.state")}
                        <select
                          value={member.state}
                          onChange={(event) =>
                            setMembers((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                state: event.target.value,
                              })),
                            )
                          }
                          className="mt-1 block min-h-11 w-full rounded-control border border-border bg-surface-solid px-2.5 py-2 text-sm text-foreground outline-none focus:border-accent-teal/60"
                        >
                          {memberStates.map((state) => (
                            <option key={state} value={state}>
                              {t(
                                `panels.teamsPanels.studio.stateOptions.${state}`,
                              )}
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                  </div>
                ))}
              </div>
              <button
                type="button"
                onClick={() =>
                  setMembers((current) => [
                    ...current,
                    {
                      id: nextId(
                        "member",
                        current.map((member) => member.id),
                      ),
                      name: "",
                      role: "",
                      profile: "",
                      capabilities: "",
                      session_id: "",
                      state: "idle",
                    },
                  ])
                }
                className="mt-3 inline-flex min-h-11 items-center gap-1.5 rounded-control border border-border px-3 py-2 text-xs font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
              >
                <PlusIcon size={13} aria-hidden="true" />
                {t("panels.teamsPanels.studio.addMember")}
              </button>
            </fieldset>

            <fieldset className="min-w-0 rounded-card border border-border bg-surface-softer p-3">
              <legend className="px-1 text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                {t("panels.teamsPanels.studio.tasksTitle")}
              </legend>
              <p className="mb-3 mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.teamsPanels.studio.tasksHint")}
              </p>
              <div className="space-y-3">
                {tasks.map((task, index) => (
                  <div
                    key={`${task.id}-${index}`}
                    className="rounded-control border border-border bg-surface-solid p-2.5"
                  >
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <span className="text-xs font-semibold text-foreground">
                        {t("panels.teamsPanels.studio.taskLabel", {
                          count: index + 1,
                        })}
                      </span>
                      {tasks.length > 1 ? (
                        <button
                          type="button"
                          onClick={() =>
                            setTasks((current) =>
                              current.filter((_, itemIndex) => itemIndex !== index),
                            )
                          }
                          className="inline-flex min-h-11 items-center gap-1 rounded-control border border-border px-2 py-1 text-xs text-muted-foreground transition hover:border-accent-red/40 hover:text-accent-red"
                          aria-label={t(
                            "panels.teamsPanels.studio.removeTask",
                            { count: index + 1 },
                          )}
                        >
                          <Trash2Icon size={13} aria-hidden="true" />
                          {t("panels.teamsPanels.studio.remove")}
                        </button>
                      ) : null}
                    </div>
                    <div className="grid gap-2 sm:grid-cols-2">
                      <StudioInput
                        label={t("panels.teamsPanels.studio.id")}
                        value={task.id}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              id: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.assignee")}
                        value={task.assignee}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              assignee: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.priority")}
                        type="number"
                        value={task.priority}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              priority: value,
                            })),
                          )
                        }
                      />
                      <label className="block text-xs text-muted-foreground">
                        {t("panels.teamsPanels.studio.difficulty")}
                        <select
                          value={task.difficulty}
                          onChange={(event) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                difficulty: event.target.value,
                              })),
                            )
                          }
                          className="mt-1 block min-h-11 w-full rounded-control border border-border bg-surface-solid px-2.5 py-2 text-sm text-foreground outline-none focus:border-accent-teal/60"
                        >
                          {taskDifficulties.map((difficulty) => (
                            <option key={difficulty} value={difficulty}>
                              {t(
                                `panels.teamsPanels.studio.difficultyOptions.${difficulty}`,
                              )}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label className="block text-xs text-muted-foreground">
                        {t("panels.teamsPanels.studio.taskType")}
                        <select
                          value={task.task_type}
                          onChange={(event) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                task_type: event.target.value,
                              })),
                            )
                          }
                          className="mt-1 block min-h-11 w-full rounded-control border border-border bg-surface-solid px-2.5 py-2 text-sm text-foreground outline-none focus:border-accent-teal/60"
                        >
                          <option value="">
                            {t("panels.teamsPanels.studio.taskTypeNone")}
                          </option>
                          {taskTypes.map((taskType) => (
                            <option key={taskType} value={taskType}>
                              {t(
                                `panels.teamsPanels.studio.taskTypeOptions.${taskType}`,
                              )}
                            </option>
                          ))}
                        </select>
                      </label>
                      <div className="sm:col-span-2">
                        <StudioInput
                          label={t("panels.teamsPanels.studio.titleField")}
                          value={task.title}
                          onChange={(value) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                title: value,
                              })),
                            )
                          }
                        />
                      </div>
                      <div className="sm:col-span-2">
                        <StudioInput
                          label={t("panels.teamsPanels.studio.goalField")}
                          value={task.goal}
                          onChange={(value) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                goal: value,
                              })),
                            )
                          }
                        />
                      </div>
                      <div className="sm:col-span-2">
                        <StudioInput
                          label={t("panels.teamsPanels.studio.taskSubject")}
                          value={task.task_subject}
                          onChange={(value) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                task_subject: value,
                              })),
                            )
                          }
                        />
                      </div>
                      <div className="sm:col-span-2">
                        <StudioInput
                          label={t(
                            "panels.teamsPanels.studio.difficultyRationale",
                          )}
                          value={task.difficulty_rationale}
                          onChange={(value) =>
                            setTasks((current) =>
                              updateAt(current, index, (item) => ({
                                ...item,
                                difficulty_rationale: value,
                              })),
                            )
                          }
                        />
                      </div>
                      <StudioInput
                        label={t("panels.teamsPanels.studio.inputs")}
                        value={task.inputs}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              inputs: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.readPaths")}
                        value={task.read_paths}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              read_paths: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.writePaths")}
                        value={task.write_paths}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              write_paths: value,
                            })),
                          )
                        }
                      />
                      <StudioInput
                        label={t("panels.teamsPanels.studio.deliverables")}
                        value={task.deliverables}
                        onChange={(value) =>
                          setTasks((current) =>
                            updateAt(current, index, (item) => ({
                              ...item,
                              deliverables: value,
                            })),
                          )
                        }
                      />
                    </div>
                  </div>
                ))}
              </div>
              <button
                type="button"
                onClick={() =>
                  setTasks((current) => [
                    ...current,
                    {
                      id: nextId("task", current.map((task) => task.id)),
                      title: "",
                      goal: "",
                      assignee: "",
                      priority: "0",
                      difficulty: "normal",
                      difficulty_rationale: "",
                      task_type: "",
                      task_subject: "",
                      inputs: "",
                      read_paths: "",
                      write_paths: "",
                      deliverables: "",
                    },
                  ])
                }
                className="mt-3 inline-flex min-h-11 items-center gap-1.5 rounded-control border border-border px-3 py-2 text-xs font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
              >
                <PlusIcon size={13} aria-hidden="true" />
                {t("panels.teamsPanels.studio.addTask")}
              </button>
            </fieldset>
          </div>

          <fieldset className="mt-4 rounded-card border border-border bg-surface-softer p-3">
            <legend className="px-1 text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              {t("panels.teamsPanels.studio.dependenciesTitle")}
            </legend>
            <p className="mb-3 mt-1 text-xs leading-5 text-muted-foreground">
              {t("panels.teamsPanels.studio.dependenciesHint")}
            </p>
            <div className="space-y-2">
              {dependencies.map((dependency, index) => (
                <div
                  key={`dependency-${index}`}
                  className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
                >
                  <StudioInput
                    label={t("panels.teamsPanels.studio.taskIdField")}
                    value={dependency.task_id}
                    onChange={(value) =>
                      setDependencies((current) =>
                        updateAt(current, index, (item) => ({
                          ...item,
                          task_id: value,
                        })),
                      )
                    }
                  />
                  <StudioInput
                    label={t("panels.teamsPanels.studio.dependsOnField")}
                    value={dependency.depends_on_id}
                    onChange={(value) =>
                      setDependencies((current) =>
                        updateAt(current, index, (item) => ({
                          ...item,
                          depends_on_id: value,
                        })),
                      )
                    }
                  />
                  <button
                    type="button"
                    onClick={() =>
                      setDependencies((current) =>
                        current.filter((_, itemIndex) => itemIndex !== index),
                      )
                    }
                    className="mt-5 inline-flex min-h-11 items-center justify-center gap-1 rounded-control border border-border px-2 py-1 text-xs text-muted-foreground transition hover:border-accent-red/40 hover:text-accent-red"
                    aria-label={t(
                      "panels.teamsPanels.studio.removeDependency",
                      { count: index + 1 },
                    )}
                  >
                    <Trash2Icon size={13} aria-hidden="true" />
                    {t("panels.teamsPanels.studio.remove")}
                  </button>
                </div>
              ))}
            </div>
            <button
              type="button"
              onClick={() =>
                setDependencies((current) => [
                  ...current,
                  { task_id: "", depends_on_id: "" },
                ])
              }
              className="mt-3 inline-flex min-h-11 items-center gap-1.5 rounded-control border border-border px-3 py-2 text-xs font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
            >
              <PlusIcon size={13} aria-hidden="true" />
              {t("panels.teamsPanels.studio.addDependency")}
            </button>
          </fieldset>

          <div className="mt-4 flex flex-col gap-2 border-t border-border pt-3 sm:flex-row sm:items-center sm:justify-between">
            <p className="max-w-3xl text-xs leading-5 text-muted-foreground">
              {t("panels.teamsPanels.studio.serverBoundary")}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={onCancel}
                className="min-h-11 rounded-control border border-border px-3 py-2 text-sm font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
              >
                {t("panels.teamsPanels.studio.cancel")}
              </button>
              <button
                type="button"
                onClick={() => void handleCreate()}
                disabled={isCreating}
                className="min-h-11 rounded-control border border-accent-teal/35 bg-accent-teal/10 px-3 py-2 text-sm font-medium text-foreground transition hover:bg-accent-teal/20 disabled:cursor-wait disabled:opacity-60"
              >
                {isCreating
                  ? t("panels.teamsPanels.studio.creating")
                  : t("panels.teamsPanels.studio.create")}
              </button>
            </div>
          </div>
          {error ? (
            <p
              role="alert"
              className="mt-3 rounded-control border border-accent-red/25 bg-accent-red/8 px-3 py-2 text-sm leading-6 text-muted-foreground"
            >
              {error}
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}

function StudioInput({
  label,
  onChange,
  type = "text",
  value,
}: {
  label: string;
  onChange: (value: string) => void;
  type?: "number" | "text";
  value: string;
}) {
  return (
    <label className="block text-xs text-muted-foreground">
      {label}
      <input
        type={type}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="mt-1 block min-h-11 w-full rounded-control border border-border bg-surface-solid px-2.5 py-2 text-sm text-foreground outline-none focus:border-accent-teal/60"
      />
    </label>
  );
}
