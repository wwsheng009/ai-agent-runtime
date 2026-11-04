// Shell execution environment copy (`editor.shell.*`).
//
// Distinct from editor-shell.ts: that file holds the "backend config workspace"
// page shell; this one is about which shell runs commands.

import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorExecShell } from "../../zh-CN/runtime-config/editor-exec-shell";

export const enRuntimeConfigEditorExecShell = {
  shell: {
    title: "Shell execution environment",
    description:
      "Decides which shell runs tool commands. With a POSIX shell selected on Windows the model can write bash instead of PowerShell, so scripts no longer need per-platform forks.",
    enabled: "Enabled",
    disabled: "Disabled",
    autoDetectHint:
      "When on, candidates are probed in order and the first passing probe wins; when off, only the forced path below is used.",
    strictProbeHint:
      "In strict mode a failing probe reports an error instead of falling back to the previous candidate. Keep it on to avoid silently degrading into the wrong dialect.",
    badges: {
      autoOn: "Auto-detect",
      autoOff: "Manual",
      candidateCount: "{{count}} candidates",
      pinned: "Pinned path",
      builtin: "Builtin chain",
    },
    fields: {
      autoDetect: "Auto-detect",
      strictProbe: "Strict probe",
      path: "Forced path",
      probeTimeout: "Probe timeout",
      scriptDelivery: "Script delivery",
    },
    placeholders: {
      path: "C:\\Program Files\\Git\\bin\\bash.exe",
      probeTimeout: "3s",
    },
    deliveryOptions: {
      stdin: "stdin: the script goes to standard input and is not bound by command-line length",
      argv: "argv: the script is inlined into the command-line arguments",
    },
    prefer: {
      title: "Candidate order",
      hint: "Tried top to bottom",
      empty:
        "No candidate chain configured; the builtin chain is used (git-bash → msys-bash → busybox → pwsh).",
      addLabel: "Available:",
      moveUp: "Move up",
      moveDown: "Move down",
      remove: "Remove from chain",
      current: "Effective chain: {{chain}}",
      builtin: "Builtin chain",
    },
    probe: {
      title: "Live probe",
      description:
        "Run the probe once against the current draft to see which shell actually works here. Configuration is not saved.",
      action: "Probe",
      running: "Probing…",
      idle: "No probe result yet — hit “Probe” in the top right.",
      effective: "Effective: {{shell}} ({{path}})",
      effectiveMeta: "Dialect {{dialect}} · source {{source}} · {{ms}}ms",
    },
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorExecShell>;
