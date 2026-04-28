export type MessageSegment =
  | {
      type: "text";
      content: string;
    }
  | {
      type: "code";
      language: "bash" | "json" | "tsx" | "ts" | "html";
      code: string;
      title?: string;
    }
  | {
      type: "checklist";
      title: string;
      items: string[];
    }
  | {
      type: "receipt";
      title: string;
      items: Array<{
        label: string;
        value: string;
        tone?: "accent" | "warning" | "muted";
      }>;
    }
  | {
      type: "callout";
      title: string;
      content: string;
      tone?: "info" | "warning" | "success";
    }
  | {
      type: "image";
      src: string;
      alt?: string;
      caption?: string;
      width?: number;
      height?: number;
      artifactId?: string;
      imageId?: string;
    }
  | {
      type: "image-placeholder";
      imageId: string;
      phase: "started" | "partial" | "completed" | "failed";
      progress?: number;
      caption?: string;
      errorMessage?: string;
    };

export type ChatMessage = {
  id: string;
  role: "user" | "assistant";
  author: string;
  label: string;
  segments: MessageSegment[];
  relatedArtifactIds?: string[];
};

export type Artifact = {
  id: string;
  name: string;
  path: string;
  summary: string;
  kind: "code" | "html" | "json" | "image";
  language?: "json" | "tsx" | "ts" | "html";
  content: string;
  previewHtml?: string;
  mimeType?: string;
  byteCount?: number;
  sha256?: string;
  revisedPrompt?: string;
};

export type Thread = {
  id: string;
  title: string;
  summary: string;
  updatedAt: string;
  status: "active" | "draft" | "review";
  sessionId?: string;
  transport?: "mock" | "live" | "error";
  runtimeSource?: string;
  runtimeEventCount?: number;
  lastRuntimeEventType?: string;
  lastError?: string | null;
  tags: string[];
  prompts: string[];
  messages: ChatMessage[];
  artifacts: Artifact[];
};

const workspaceShellArtifact = `import { MessageComposer } from "@/components/workspace/message-composer";
import { MessageList } from "@/components/workspace/message-list";
import { ArtifactPanel } from "@/components/workspace/artifact-panel";
import { WorkspaceSidebar } from "@/components/workspace/workspace-sidebar";

export function WorkspaceShell() {
  return (
    <div className="grid min-h-0 flex-1 gap-4 xl:grid-cols-[18rem_minmax(0,1fr)_24rem]">
      <WorkspaceSidebar />
      <section className="surface-panel min-h-[42rem]">
        <MessageList />
        <MessageComposer />
      </section>
      <ArtifactPanel />
    </div>
  );
}`;

const routeContractArtifact = `{
  "canonical_chat": {
    "method": "POST",
    "path": "/api/agent/chat",
    "notes": [
      "single canonical entrypoint",
      "suitable for first web console integration"
    ]
  },
  "session_runtime": {
    "method": "GET",
    "path": "/api/runtime/sessions/{id}/runtime/stream",
    "notes": [
      "wired into the current workspace shell",
      "used for runtime event visibility and artifact updates"
    ]
  },
  "teams": {
    "method": "GET",
    "path": "/api/runtime/teams",
    "notes": [
      "separate advanced workflow",
      "not required for phase-one shell"
    ]
  }
}`;

const landingPreviewArtifact = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>AI Agent Runtime Landing Preview</title>
    <style>
      body {
        margin: 0;
        font-family: "Segoe UI", sans-serif;
        background:
          radial-gradient(circle at top, rgba(240, 184, 71, 0.24), transparent 34%),
          linear-gradient(180deg, #111212 0%, #1b1d1c 56%, #111212 100%);
        color: #f5f1e8;
      }
      .hero {
        min-height: 100vh;
        display: grid;
        place-items: center;
        padding: 48px;
      }
      .panel {
        width: min(880px, 100%);
        border: 1px solid rgba(255, 255, 255, 0.12);
        border-radius: 28px;
        padding: 40px;
        background: rgba(10, 11, 11, 0.66);
        box-shadow: 0 30px 120px rgba(0, 0, 0, 0.36);
      }
      .kicker {
        display: inline-flex;
        gap: 12px;
        border-radius: 999px;
        padding: 10px 16px;
        background: rgba(255, 255, 255, 0.08);
        color: #f0c77b;
        text-transform: uppercase;
        letter-spacing: 0.14em;
        font-size: 0.75rem;
      }
      h1 {
        margin: 20px 0 14px;
        font-size: clamp(2.75rem, 8vw, 5.5rem);
        line-height: 0.94;
      }
      p {
        max-width: 56ch;
        color: rgba(245, 241, 232, 0.72);
        font-size: 1.125rem;
        line-height: 1.7;
      }
      .rail {
        display: grid;
        grid-template-columns: repeat(3, 1fr);
        gap: 16px;
        margin-top: 30px;
      }
      .metric {
        border-radius: 22px;
        background: rgba(255, 255, 255, 0.05);
        padding: 18px;
      }
      .metric b {
        display: block;
        margin-bottom: 8px;
        font-size: 0.75rem;
        color: rgba(245, 241, 232, 0.6);
        text-transform: uppercase;
      }
    </style>
  </head>
  <body>
    <main class="hero">
      <section class="panel">
        <span class="kicker">Vite Shell · Reused DeerFlow UI</span>
        <h1>AI Agent Runtime</h1>
        <p>
          Reuse the visual shell, keep the contract local, and wire the runtime
          in phases. Landing, workspace chrome, message list, input box and
          artifact panes are ready before deeper API integration starts.
        </p>
        <div class="rail">
          <div class="metric">
            <b>Phase one</b>
            Visual shell and mock interaction
          </div>
          <div class="metric">
            <b>Phase two</b>
            /api/agent/chat and session history
          </div>
          <div class="metric">
            <b>Phase three</b>
            runtime events and teams
          </div>
        </div>
      </section>
    </main>
  </body>
</html>`;

export const initialThreads: Thread[] = [
  {
    id: "thread-shell",
    title: "Frontend shell migration",
    summary:
      "Keep Vite, reuse DeerFlow visual structure, and delay runtime protocol binding.",
    updatedAt: "2026-03-31T09:10:00+08:00",
    status: "active",
    transport: "mock",
    runtimeEventCount: 0,
    lastError: null,
    tags: ["vite", "workspace", "ui"],
    prompts: [
      "Draft a /api/agent/chat adapter",
      "Show how artifacts should open on the right",
      "Explain which DeerFlow modules stay out",
    ],
    artifacts: [
      {
        id: "artifact-workspace-shell",
        name: "workspace-shell.tsx",
        path: "src/components/workspace/workspace-shell.tsx",
        summary: "Three-column shell for sidebar, messages, and artifacts.",
        kind: "code",
        language: "tsx",
        content: workspaceShellArtifact,
      },
      {
        id: "artifact-route-contract",
        name: "runtime-route-contract.json",
        path: "docs/frontend/runtime-route-contract.json",
        summary: "Canonical routes to bind after the visual shell is accepted.",
        kind: "json",
        language: "json",
        content: routeContractArtifact,
      },
      {
        id: "artifact-landing-preview",
        name: "landing-preview.html",
        path: "artifacts/landing-preview.html",
        summary: "A standalone preview for the landing art direction.",
        kind: "html",
        language: "html",
        content: landingPreviewArtifact,
        previewHtml: landingPreviewArtifact,
      },
    ],
    messages: [
      {
        id: "msg-1",
        role: "user",
        author: "You",
        label: "scope",
        segments: [
          {
            type: "text",
            content:
              "Reuse the DeerFlow UI shell, keep the current repo on Vite, and do not pull in LangGraph or Next.js server routes.",
          },
        ],
      },
      {
        id: "msg-2",
        role: "assistant",
        author: "Runtime design",
        label: "decision",
        relatedArtifactIds: [
          "artifact-workspace-shell",
          "artifact-route-contract",
        ],
        segments: [
          {
            type: "text",
            content:
              "The safe migration split is visual shell first and runtime binding second.\n\nThe workspace can already feel like Deer Flow if the center canvas, runtime receipts, and artifact references are composed at the message level instead of being pushed into shell-level panels.",
          },
          {
            type: "receipt",
            title: "Migration split",
            items: [
              {
                label: "Phase one",
                value: "Landing and workspace shell",
                tone: "accent",
              },
              {
                label: "Phase two",
                value: "Runtime adapters and session sync",
              },
              {
                label: "Constraint",
                value: "Keep Vite and local routes",
                tone: "muted",
              },
            ],
          },
          {
            type: "checklist",
            title: "Keep in the browser layer",
            items: [
              "Layout, sidebar, and message canvas",
              "Composer surface and prompt shortcuts",
              "Artifact references and generated file receipts",
            ],
          },
          {
            type: "code",
            language: "bash",
            title: "Phase-one contract target",
            code: [
              "POST /api/agent/chat",
              "GET  /api/runtime/sessions/{id}/runtime/stream",
              "GET  /api/runtime/sessions/{id}/history",
            ].join("\n"),
          },
          {
            type: "callout",
            title: "Hold line on protocol reuse",
            tone: "warning",
            content:
              "Low-reuse Next.js and LangGraph server modules stay out until the Vite shell is stable.",
          },
        ],
      },
      {
        id: "msg-3",
        role: "user",
        author: "You",
        label: "constraint",
        segments: [
          {
            type: "text",
            content:
              "Do not drift from the original migration direction. The frontend should still look like a Vite app with local routes and a thin adapter layer.",
          },
        ],
      },
      {
        id: "msg-4",
        role: "assistant",
        author: "UI scaffold",
        label: "plan",
        relatedArtifactIds: ["artifact-landing-preview"],
        segments: [
          {
            type: "text",
            content:
              "That means the shell can already feel close to DeerFlow without copying its protocol model.\n\nLanding, workspace chrome, the message surface, and the artifact rail can all be delivered with local mock data first. The adapter later maps these views onto /api/agent/chat and session runtime events.",
          },
          {
            type: "receipt",
            title: "Runtime attachment plan",
            items: [
              {
                label: "Canonical chat",
                value: "POST /api/agent/chat",
                tone: "accent",
              },
              {
                label: "History sync",
                value: "GET /api/runtime/sessions/{id}/history",
              },
              {
                label: "Runtime stream",
                value: "GET /api/runtime/sessions/{id}/runtime/stream",
              },
            ],
          },
          {
            type: "callout",
            title: "Mock-first review",
            tone: "info",
            content:
              "Use local mock data to refine the message hierarchy before binding the transport layer.",
          },
        ],
      },
    ],
  },
  {
    id: "thread-agent-chat",
    title: "Agent chat adapter sketch",
    summary:
      "Bridge the canonical chat entrypoint into the Vite workspace without importing DeerFlow threads.",
    updatedAt: "2026-03-30T20:50:00+08:00",
    status: "review",
    transport: "mock",
    runtimeEventCount: 0,
    lastError: null,
    tags: ["agent-chat", "api"],
    prompts: [
      "Map assistant messages into the workspace list",
      "Prototype streaming chunks",
      "Document optimistic UI constraints",
    ],
    artifacts: [
      {
        id: "artifact-adapter-outline",
        name: "agent-chat-adapter.ts",
        path: "src/runtime/agent-chat-adapter.ts",
        summary: "Translate canonical runtime responses into local UI state.",
        kind: "code",
        language: "ts",
        content: [
          "export async function sendAgentChat(input: AgentChatPayload) {",
          '  const response = await fetch("/api/agent/chat", {',
          '    method: "POST",',
          '    headers: { "Content-Type": "application/json" },',
          "    body: JSON.stringify(input),",
          "  });",
          "",
          "  if (!response.ok) {",
          '    throw new Error("agent chat request failed");',
          "  }",
          "",
          "  return response.json();",
          "}",
        ].join("\n"),
      },
    ],
    messages: [
      {
        id: "msg-a1",
        role: "assistant",
        author: "Protocol note",
        label: "adapter",
        segments: [
          {
            type: "text",
            content:
              "The first production adapter should map one request-response cycle onto local thread state. Do not design around DeerFlow thread ids; design around runtime session ids only when the backend is ready to expose them to the browser.",
          },
        ],
      },
    ],
  },
  {
    id: "thread-artifacts",
    title: "Artifact rail prototype",
    summary:
      "Keep the right-side artifact pane interactive, even before backend file delivery is wired in.",
    updatedAt: "2026-03-29T15:40:00+08:00",
    status: "draft",
    transport: "mock",
    runtimeEventCount: 0,
    lastError: null,
    tags: ["artifacts", "preview"],
    prompts: [
      "Open HTML in preview mode",
      "Switch between code and preview",
      "Keep file metadata compact",
    ],
    artifacts: [
      {
        id: "artifact-html-report",
        name: "report-preview.html",
        path: "outputs/report-preview.html",
        summary: "HTML output rendered inline in the artifact pane.",
        kind: "html",
        language: "html",
        content: landingPreviewArtifact,
        previewHtml: landingPreviewArtifact,
      },
    ],
    messages: [
      {
        id: "msg-r1",
        role: "assistant",
        author: "Artifact rail",
        label: "preview",
        relatedArtifactIds: ["artifact-html-report"],
        segments: [
          {
            type: "text",
            content:
              "Artifact preview can stay frontend-only in the first pass. HTML uses iframe srcDoc, while source view stays in a code block. Once runtime file delivery exists, the same component swaps local strings for fetched file content.",
          },
        ],
      },
    ],
  },
];
