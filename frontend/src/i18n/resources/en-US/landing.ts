// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../shape";
import type { zhLanding } from "../zh-CN/landing";

export const enLanding = {
  header: {
    productLabel: "Agent Workspace Platform",
    productName: "AI Agent Runtime",
    productTour: "Product tour",
    openWorkspace: "Open workspace",
  },
  hero: {
    eyebrow: "Browser-native AI workspace for research, execution, and review",
    rotatingWord1: "Deep Research",
    rotatingWord2: "Agent Workspaces",
    rotatingWord3: "Artifact Reviews",
    rotatingWord4: "Runtime Teams",
    titlePrefix: "Research, orchestrate, and ship",
    titleSuffix: "from one browser-native workspace.",
    body:
      "AI Agent Runtime brings live threads, runtime events, artifact evidence, and teammate coordination into a single product surface.",
    primaryCta: "Enter workspace",
    secondaryCta: "See product highlights",
    unifiedFlowTitle: "Unified flow",
    unifiedFlowBody:
      "Move from prompt to action to review without jumping across disconnected tools or hidden execution surfaces.",
    teamReadyTitle: "Team-ready workspace",
    teamReadyBody:
      "Keep thread context, runtime teams, and operational detail in the same shell so handoffs stay readable.",
    verifiableOutputTitle: "Verifiable output",
    verifiableOutputBody:
      "Inspect artifacts, stream runtime events, and keep the evidence next to the work that produced it.",
    snapshotEyebrow: "Product snapshot",
    snapshotTitle: "One shell, three visible layers",
    snapshotBody:
      "Keep discovery, active work, and runtime evidence legible at the same time: the product story up front, the active thread in the middle, and the operational detail around it.",
    productSiteTitle: "Product site",
    productSiteBody:
      "A clear product narrative explains what the workspace does, where it fits, and why teams can trust the output.",
    workspaceEntryTitle: "Workspace entry",
    workspaceEntryBody:
      "Start with /workspace and land directly in an active thread, so the app feels immediate instead of route-driven.",
    runtimeEvidenceTitle: "Runtime evidence",
    runtimeEvidenceBody1: "Chat turns and replies stay attached to the thread.",
    runtimeEvidenceBody2: "History sync keeps the workspace aligned with the session.",
    runtimeEvidenceBody3: "Runtime streams expose tool calls, routes, and artifacts.",
  },
  deferred: {
    productHighlights: "Product highlights",
  },
  community: {
    eyebrow: "Community",
    title: "Bring teams, evidence, and runtime control into one flow",
    subtitle:
      "AI Agent Runtime is built for teams that want a product-grade workspace without giving up operational detail. The landing page introduces the flow; the workspace carries it through execution.",
    pillarsBadge: "Product pillars",
    layersBadge: "three visible layers",
    pillars: {
      focusedWork: {
        title: "Focused work",
        summary:
          "Start with a clear thread, gather only the context that matters, and keep the task moving without losing the original request.",
      },
      sharedVisibility: {
        title: "Shared visibility",
        summary:
          "Keep teammates, runtime events, and task dispatch readable from one operating surface instead of scattering work across tabs.",
      },
      inspectableResults: {
        title: "Inspectable results",
        summary:
          "Review artifacts, receipts, and streamed execution detail without losing the thread that explains why the work happened.",
      },
    },
    readyBadge: "Ready to start",
    readyTitle:
      "Open the workspace and carry the full thread from request to review.",
    readyPoint1:
      "Stay inside one operating surface for prompts, runtime context, team coordination, and artifact review.",
    readyPoint2:
      "Use the product tour first, then jump directly into the active workspace when you are ready to act.",
    launchWorkspace: "Launch workspace",
    browseHighlights: "Browse product highlights",
  },
  caseStudy: {
    eyebrow: "Product highlights",
    title: "See how the workspace turns agent work into something reviewable",
    subtitle:
      "Each card maps to a real surface in AI Agent Runtime: active threads, runtime teams, streamed execution, and artifact detail that stays attached to the work.",
    exploreInWorkspace: "Explore in workspace",
    cards: {
      liveExecution: {
        label: "Live execution",
        title: "Follow the full turn lifecycle in one workspace",
        description:
          "Trace a live agent turn from submit to completion while session history and runtime events keep feeding the same thread.",
      },
      teamCoordination: {
        label: "Team coordination",
        title:
          "Coordinate multiple runtime teammates from the same control rail",
        description:
          "Keep multi-team summaries, teammate readiness, and dispatch detail visible without leaving the working thread.",
      },
      artifactDetail: {
        label: "Artifact detail",
        title: "Inspect outputs like a product surface, not a JSON dump",
        description:
          "Switch between source and preview without losing the thread, so evidence and output stay close to the messages that produced them.",
      },
      agentReasoning: {
        label: "Agent reasoning",
        title: "See how the assistant moved from plan to execution",
        description:
          "Map planning, routing, orchestration, and tool events into a readable message stream with attached receipts.",
      },
      operationalClarity: {
        label: "Operational clarity",
        title: "Keep the runtime legible without hiding the underlying system",
        description:
          "Expose chat, session, and runtime capabilities through a clear product surface with explicit operational controls.",
      },
      productExperience: {
        label: "Product experience",
        title: "Keep the website and workspace visually connected",
        description:
          "Treat the landing page and workspace as connected surfaces: one explains the value, the other lets teams act on it.",
      },
    },
  },
  sandbox: {
    eyebrow: "Runtime environment",
    title: "A real execution environment behind every agent turn",
    subtitle:
      "Agents need files, commands, session state, and runtime feedback to do useful work. AI Agent Runtime keeps those surfaces explicit, so teams can inspect what happened instead of guessing after the fact.",
    terminalLabel: "Runtime terminal",
    featuresLabel: "Runtime features",
    featuresTitle:
      "Explicit control surfaces for threads, tools, artifacts, and teams.",
    featuresBody1:
      "The workspace is designed to feel operational, not decorative. You can send turns, inspect history, follow runtime events, and review outputs from the same place.",
    featuresBody2:
      "Runtime APIs stay visible where that matters, but the product surface stays calm enough for daily use by engineers, operators, and reviewers.",
    surfacesLabel: "Available surfaces",
    tags: {
      shellAccess: "Shell access",
      workspaceFiles: "Workspace files",
      sessionHistory: "Session history",
      runtimeSse: "Runtime SSE",
      artifactPreview: "Artifact preview",
    },
  },
  skills: {
    eyebrow: "Core capabilities",
    title: "Capabilities that stay visible while agents work",
    subtitle:
      "AI Agent Runtime keeps the workflow legible as work moves from understanding to coordination to delivery. The same workspace holds the thread, the supporting context, and the runtime evidence.",
    ladderLabel: "Capability ladder",
    stageLabel: "Stage {{index}}",
    columns: {
      understand: {
        title: "Understand",
        description:
          "Start with the thread, repository, and supporting evidence that matter, so the workspace stays focused on the job instead of dumping raw context everywhere.",
        items: [
          "Repository scan",
          "Targeted reads",
          "Evidence-first context",
        ],
      },
      coordinate: {
        title: "Coordinate",
        description:
          "Keep active tasks, teammate state, and runtime signals aligned so the whole team can see what is blocked, what is running, and what needs attention.",
        items: ["Thread planning", "Team handoff", "Runtime checkpoints"],
      },
      deliver: {
        title: "Deliver",
        description:
          "Edit, inspect, and verify from the same workspace, with artifacts and execution detail staying next to the messages that created them.",
        items: ["Code changes", "Artifact previews", "Verification loops"],
      },
    },
  },
  whatsNew: {
    eyebrow: "Why it works",
    title: "Everything needed to move from prompt to verified output",
    subtitle:
      "The landing page now explains the product in user terms: how you enter the workspace, how execution stays visible, and how artifacts remain tied to the thread that produced them.",
    cards: {
      productShell: {
        label: "Product shell",
        title: "Structured for first-time understanding",
        description:
          "A clear sequence of hero, highlights, capabilities, runtime detail, and CTA helps explain the product before a user enters the workspace.",
      },
      workspaceEntry: {
        label: "Workspace entry",
        title: "A cleaner path into active work",
        description:
          "Primary calls to action now land on /workspace, reducing route noise and getting users into the active work surface faster.",
      },
      runtimeSignal: {
        label: "Runtime",
        title: "Live signals stay attached to the thread",
        description:
          "Prompt submission, session history sync, and runtime streaming keep the thread current while work is still in flight.",
      },
      artifactEvidence: {
        label: "Artifacts",
        title: "Evidence remains inspectable",
        description:
          "Planning, orchestration, route, subagent, and tool payloads stay attached back into inspectable artifacts and receipts.",
      },
      conversation: {
        label: "Conversation",
        title: "Thread-first workspace flow",
        description:
          "The message surface keeps focusing on the selected thread instead of scattering execution state across disconnected screens.",
      },
      extension: {
        label: "Extension",
        title: "Ready to grow with the runtime",
        description:
          "The structure leaves room for richer markdown, deeper artifact review, and more advanced team controls without changing the core flow.",
      },
    },
  },
  footer: {
    quote:
      "\"Keep the thread clear, the runtime visible, and the output reviewable.\"",
    body:
      "AI Agent Runtime gives teams a product-grade entry point into active work, with threads, runtime events, and artifacts kept close enough to support real review and handoff.",
    openWorkspace: "Open the workspace",
    viewProductHighlights: "View product highlights",
  },
} satisfies DeepStringShape<typeof zhLanding>;
