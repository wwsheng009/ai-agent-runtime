import {
  BookMarkedIcon,
  FolderGit2Icon,
  RocketIcon,
  WrenchIcon,
} from "lucide-react";

import { Section } from "@/components/landing/section";

const skillColumns = [
  {
    title: "Understand",
    description:
      "Start with the thread, repository, and supporting evidence that matter, so the workspace stays focused on the job instead of dumping raw context everywhere.",
    items: ["Repository scan", "Targeted reads", "Evidence-first context"],
  },
  {
    title: "Coordinate",
    description:
      "Keep active tasks, teammate state, and runtime signals aligned so the whole team can see what is blocked, what is running, and what needs attention.",
    items: ["Thread planning", "Team handoff", "Runtime checkpoints"],
  },
  {
    title: "Deliver",
    description:
      "Edit, inspect, and verify from the same workspace, with artifacts and execution detail staying next to the messages that created them.",
    items: ["Code changes", "Artifact previews", "Verification loops"],
  },
];

export function SkillsSection() {
  return (
    <Section
      className="w-full"
      eyebrow="Core capabilities"
      title="Capabilities that stay visible while agents work"
      subtitle={
        <>
          AI Agent Runtime keeps the workflow legible as work moves from
          understanding to coordination to delivery. The same workspace holds
          the thread, the supporting context, and the runtime evidence.
        </>
      }
    >
      <div className="grid gap-4 lg:grid-cols-[0.92fr_1.08fr]">
        <div className="rounded-[1.9rem] border border-white/8 bg-black/22 p-6">
          <div className="flex items-center gap-3 text-sm font-semibold text-[#8fd0c6]">
            <BookMarkedIcon size={18} />
            Capability ladder
          </div>
          <div className="mt-6 space-y-4">
            {skillColumns.map((column, index) => (
              <div
                key={column.title}
                className="relative overflow-hidden rounded-[1.4rem] border border-white/8 bg-white/4 px-4 py-4"
              >
                <div className="absolute top-0 left-0 h-full w-1 bg-gradient-to-b from-[#f0c77b] via-[#8fd0c6] to-transparent" />
                <div className="pl-4">
                  <div className="app-text-11 uppercase tracking-[0.2em] text-[#f0c77b]">
                    Stage {index + 1}
                  </div>
                  <div className="mt-2 text-xl font-semibold">{column.title}</div>
                  <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                    {column.description}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="grid gap-4 md:grid-cols-3">
          {skillColumns.map((column, index) => (
            <div
              key={column.title}
              className="rounded-[1.8rem] border border-white/8 bg-[linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0.02))] p-5"
            >
              <div className="flex items-center gap-3">
                {index === 0 ? (
                  <FolderGit2Icon size={18} className="text-[#8fd0c6]" />
                ) : index === 1 ? (
                  <WrenchIcon size={18} className="text-[#f0c77b]" />
                ) : (
                  <RocketIcon size={18} className="text-[#8fd0c6]" />
                )}
                <div className="text-lg font-semibold">{column.title}</div>
              </div>
              <ul className="mt-5 space-y-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {column.items.map((item) => (
                  <li key={item} className="flex gap-3">
                    <span className="mt-2 size-1.5 shrink-0 rounded-full bg-[#f0c77b]" />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </div>
    </Section>
  );
}
