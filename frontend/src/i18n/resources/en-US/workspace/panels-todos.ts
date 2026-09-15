export const enWorkspacePanelsTodos = {
  ariaLabel: "Current task panel",
  title: "Current tasks",
  counts: {
    completed: "{{count}} done",
    in_progress: "{{count}} in progress",
    pending: "{{count}} pending",
  },
  current: "In progress: {{text}}",
  expand: "Expand the task list",
  collapse: "Collapse the task list",
  more: "{{count}} more",
  status: {
    pending: "Pending",
    in_progress: "In progress",
    completed: "Completed",
  },
} as const;
