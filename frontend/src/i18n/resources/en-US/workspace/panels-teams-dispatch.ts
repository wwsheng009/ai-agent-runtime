// P0-6：workspace.panels.teams-dispatch 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
import type { DeepStringShape } from "../../shape";
import type { zhWorkspacePanelsTeamsDispatch } from "../../zh-CN/workspace/panels-teams-dispatch";

export const enWorkspacePanelsTeamsDispatch = {
  summary: {
    header: "Runtime Teams",
  },
  dialog: {
    eyebrow: "Runtime teams",
    title: "Team details",
    description:
      "Dispatch, team state, tasks, events, mailbox, and path claims live here so the sidebar can stay compact.",
    closeAriaLabel: "Close runtime team details",
    loading: "Loading runtime teams content…",
  },
  dispatchView: {
    fallbackLabel: "dispatch view",
  },
  dispatchConsole: {
    title: "Multi-team next task",
    description:
      "Create one ready task across multiple active teams so their orchestrators can run in parallel.",
    selectedCount: "{{count}} selected",
  },
  provision: {
    heading: "Provision runnable teams and dispatch",
    teamCount: "Team count",
    workspaceId: "Workspace id",
    workspaceIdPlaceholder: "fanout-workspace",
    strategy: "Strategy",
    strategyPlaceholder: "parallel-fanout",
    userPrefix: "User prefix",
    userPrefixPlaceholder: "fanout-user",
    teammateNamePrefix: "Teammate name prefix",
    teammateNamePrefixPlaceholder: "Fanout Worker",
    teammateProfile: "Teammate profile",
    teammateProfilePlaceholder: "parallel execution worker",
    hint: "Each provisioned team gets a lead session, a worker session, one idle teammate, and the current next task.",
    submit: "Provision runnable teams and dispatch",
  },
  template: {
    heading: "Fan-out template",
    reviewImplementVerify: "Review / Implement / Verify",
    mirrorSameTask: "Mirror Same Task",
    mirrorDescription: "Every selected team receives the same task payload.",
    roleVariantsDescription:
      "Teams receive role-specific variants of the same next task so they execute from different angles.",
  },
  teamSelect: {
    empty: "No existing teams yet. Use the provision action above to create runnable teams and fan out the next task.",
    counts: "{{tasks}} tasks · {{teammates}} teammates",
    checkingReadiness: "checking executability...",
    readinessUnavailable: "readiness unavailable",
    executable: "executable",
    notReady: "not ready",
    unknownStatus: "unknown",
  },
  taskComposer: {
    title: "Task title",
    titlePlaceholder: "Parallel review of runtime stream stability",
    goal: "Goal",
    goalPlaceholder:
      "Have each selected team tackle the same next task from a different angle and report outcomes independently.",
    inputs: "Inputs",
    inputsPlaceholder: "spec.md\nopen questions\nexpected risks",
    deliverables: "Deliverables",
    deliverablesPlaceholder: "summary.md\npatch.diff\nvalidation notes",
    priority: "Priority",
    statusHint:
      "Tasks are created with `status=ready`, so active team orchestrators can claim and execute them.",
    submitHint:
      "Use this to fan out the same next task across multiple executable teams for parallel execution.",
    submit: "Dispatch next task",
    resultCreated: "task {{taskId}} created",
    resultFailed: "dispatch failed",
  },
  monitor: {
    title: "Dispatch monitor",
    description: "Follow task execution across the most recent fan-out batch.",
    refresh: "Refresh monitor",
    autoRefreshLabel: "auto refresh:",
    autoRefreshActive: "active",
    autoRefreshIdle: "idle",
    taskEntry: "task {{taskId}}",
    assigneeEntry: "assignee {{assignee}}",
    eventEntry: "event {{event}}",
    updatedEntry: "updated {{time}}",
    empty: "No dispatch monitor data available yet.",
  },
  batchSummary: {
    title: "Batch summary",
    attempted: "Attempted",
    created: "Created",
    failed: "Failed",
    completed: "Completed",
    inProgress: "In progress",
    pending: "Pending",
    terminal: "Terminal",
    finalSummaries: "Final summaries",
    summaryGaps: "Summary gaps",
    coverage: "Coverage",
    latestUpdate: "latest update: {{time}}",
    monitorEntries: "monitor entries: {{count}}",
    monitorStatusCount: "monitor {{status}}: {{count}}",
  },
  outcomeCompare: {
    title: "Outcome compare",
    finalSummaryCoverage: "Final summary coverage",
    waitingForSummary: "Waiting for summary",
    terminal: "terminal",
    active: "active",
    terminalWithSummary: "Terminal outcomes with summary",
    terminalMissingSummary: "Terminal outcomes missing final summary",
  },
} satisfies DeepStringShape<typeof zhWorkspacePanelsTeamsDispatch>;
