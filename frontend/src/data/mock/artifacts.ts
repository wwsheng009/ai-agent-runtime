// 由 data/mock.ts 机械拆分而来（P0-2），仅搬迁不改语义。

export const workspaceShellArtifact = `import { MessageComposer } from "@/components/workspace/message-composer";
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

export const routeContractArtifact = `{
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

export const landingPreviewArtifact = `<!doctype html>
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
