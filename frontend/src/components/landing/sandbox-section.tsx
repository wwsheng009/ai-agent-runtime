import { CommandIcon, FolderCogIcon, ShieldCheckIcon } from "lucide-react";

import { Section } from "@/components/landing/section";

const terminalLines = [
  "$ ls frontend/src",
  "components  data  hooks  lib  pages  styles",
  "$ pnpm build",
  "vite v8.0.1 building for production...",
  "$ curl -N /api/runtime/sessions/{id}/runtime/stream",
  "event: runtime  seq=41  type=tool_call",
  "$ view artifact --mode preview",
  "preview ready: artifacts/landing-preview.html",
];

const sandboxTags = [
  "Shell access",
  "Workspace files",
  "Session history",
  "Runtime SSE",
  "Artifact preview",
];

export function SandboxSection() {
  return (
    <Section
      eyebrow="Runtime environment"
      title="A real execution environment behind every agent turn"
      subtitle={
        <>
          Agents need files, commands, session state, and runtime feedback to
          do useful work. AI Agent Runtime keeps those surfaces explicit, so
          teams can inspect what happened instead of guessing after the fact.
        </>
      }
    >
      <div className="grid gap-4 lg:grid-cols-[1.05fr_0.95fr]">
        <div className="overflow-hidden rounded-[1.9rem] border border-[var(--border)] bg-[var(--terminal-shell-bg)] shadow-[0_24px_80px_rgba(0,0,0,0.24)]">
          <div className="flex items-center gap-2 border-b border-[var(--border)] px-4 py-3">
            <span className="size-2.5 rounded-full bg-[#ef4444]" />
            <span className="size-2.5 rounded-full bg-[#f59e0b]" />
            <span className="size-2.5 rounded-full bg-[#22c55e]" />
            <span className="ml-3 text-xs uppercase tracking-[0.18em] text-[var(--terminal-label)]">
              Runtime terminal
            </span>
          </div>
          <div className="app-terminal-copy space-y-3 px-5 py-5 text-[var(--terminal-text)]">
            {terminalLines.map((line, index) => (
              <div
                key={`${index}-${line}`}
                className="animate-[var(--animate-fade-up)]"
                style={{ animationDelay: `${index * 80}ms` }}
              >
                {line}
              </div>
            ))}
          </div>
        </div>

        <div className="flex flex-col justify-between rounded-[1.9rem] border border-[var(--border)] bg-[var(--panel-soft-bg)] p-6">
          <div>
            <div className="flex items-center gap-3 text-sm font-semibold text-[var(--accent-secondary)]">
              <ShieldCheckIcon size={18} />
              Runtime features
            </div>
            <h3 className="mt-5 font-serif text-4xl tracking-[-0.04em]">
              Explicit control surfaces for threads, tools, artifacts, and teams.
            </h3>
            <div className="mt-6 space-y-4 text-base leading-8 text-[var(--muted-foreground)]">
              <p>
                The workspace is designed to feel operational, not decorative.
                You can send turns, inspect history, follow runtime events, and
                review outputs from the same place.
              </p>
              <p>
                Runtime APIs stay visible where that matters, but the product
                surface stays calm enough for daily use by engineers, operators,
                and reviewers.
              </p>
            </div>
          </div>

          <div className="mt-8">
            <div className="mb-4 flex items-center gap-3 text-sm font-semibold text-[var(--accent-primary)]">
              <FolderCogIcon size={18} />
              Available surfaces
            </div>
            <div className="flex flex-wrap gap-3">
              {sandboxTags.map((tag) => (
                <span
                  key={tag}
                  className="inline-flex items-center gap-2 rounded-full border border-[var(--border)] bg-[var(--surface-soft)] px-4 py-2 text-sm text-[var(--foreground)]"
                >
                  <CommandIcon size={14} className="text-[var(--accent-primary)]" />
                  {tag}
                </span>
              ))}
            </div>
          </div>
        </div>
      </div>
    </Section>
  );
}
