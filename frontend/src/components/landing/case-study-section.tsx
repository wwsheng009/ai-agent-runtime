import {
  ArrowUpRightIcon,
  BotIcon,
  FileSearchIcon,
  LayoutTemplateIcon,
  RadioTowerIcon,
  UsersIcon,
} from "lucide-react";
import { Link } from "react-router-dom";

import { buttonVariants } from "@/components/ui/button";
import { CardTitle } from "@/components/ui/card";

import { Section } from "@/components/landing/section";
import { cn } from "@/lib/utils";

const caseStudies = [
  {
    accent: "from-[#7dcfff]/35 via-[#0f172a] to-transparent",
    description:
      "Trace a live agent turn from submit to completion while session history and runtime events keep feeding the same thread.",
    icon: RadioTowerIcon,
    label: "Live execution",
    title: "Follow the full turn lifecycle in one workspace",
  },
  {
    accent: "from-[#f0c77b]/35 via-[#201812] to-transparent",
    description:
      "Keep multi-team summaries, teammate readiness, and dispatch detail visible without leaving the working thread.",
    icon: UsersIcon,
    label: "Team coordination",
    title: "Coordinate multiple runtime teammates from the same control rail",
  },
  {
    accent: "from-[#8fd0c6]/28 via-[#091717] to-transparent",
    description:
      "Switch between source and preview without losing the thread, so evidence and output stay close to the messages that produced them.",
    icon: LayoutTemplateIcon,
    label: "Artifact detail",
    title: "Inspect outputs like a product surface, not a JSON dump",
  },
  {
    accent: "from-[#8fd0c6]/22 via-[#0d1117] to-transparent",
    description:
      "Map planning, routing, orchestration, and tool events into a readable message stream with attached receipts.",
    icon: BotIcon,
    label: "Agent reasoning",
    title: "See how the assistant moved from plan to execution",
  },
  {
    accent: "from-[#f0c77b]/25 via-[#160f0f] to-transparent",
    description:
      "Expose chat, session, and runtime capabilities through a clear product surface with explicit operational controls.",
    icon: FileSearchIcon,
    label: "Operational clarity",
    title: "Keep the runtime legible without hiding the underlying system",
  },
  {
    accent: "from-[#f97316]/24 via-[#1a1010] to-transparent",
    description:
      "Treat the landing page and workspace as connected surfaces: one explains the value, the other lets teams act on it.",
    icon: ArrowUpRightIcon,
    label: "Product experience",
    title: "Keep the website and workspace visually connected",
  },
];

export function CaseStudySection() {
  return (
    <Section
      className="pt-0"
      eyebrow="Product highlights"
      title="See how the workspace turns agent work into something reviewable"
      subtitle="Each card maps to a real surface in AI Agent Runtime: active threads, runtime teams, streamed execution, and artifact detail that stays attached to the work."
    >
      <div id="product-highlights" className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {caseStudies.map(({ accent, description, icon: Icon, label, title }) => (
          <Link
            key={title}
            to="/workspace"
            className="group relative overflow-hidden rounded-[1.8rem] border border-[var(--border)] bg-[var(--panel-solid-bg)] p-6 transition hover:-translate-y-1 hover:border-[var(--border-strong)] hover:bg-[var(--panel-solid-hover-bg)]"
          >
            <div
              className={cn(
                "pointer-events-none absolute inset-0 bg-gradient-to-br opacity-90 transition duration-300 group-hover:opacity-100",
                accent,
              )}
            />
            <div className="relative z-10 flex h-full min-h-64 flex-col justify-between">
              <div className="flex items-center justify-between gap-3">
                <span className="inline-flex items-center gap-2 rounded-full border border-[var(--border)] bg-[var(--panel-strong-bg)] px-3 py-1 app-text-11 uppercase tracking-[0.18em] text-[var(--accent-secondary)]">
                  <Icon size={14} />
                  {label}
                </span>
                <ArrowUpRightIcon
                  size={16}
                  className="text-[var(--muted-foreground)] transition group-hover:text-[var(--foreground)]"
                />
              </div>
              <div className="mt-12">
                <CardTitle className="text-2xl leading-tight sm:text-[1.75rem]">
                  {title}
                </CardTitle>
                <p className="mt-4 text-sm leading-7 text-[var(--muted-foreground)]">
                  {description}
                </p>
              </div>
              <div className="mt-6">
                <span
                  className={cn(
                    buttonVariants({ variant: "secondary", size: "sm" }),
                    "border-[var(--border)] bg-[var(--panel-strong-bg)] hover:bg-[var(--surface-soft-hover)]",
                  )}
                >
                  Explore in workspace
                </span>
              </div>
            </div>
          </Link>
        ))}
      </div>
    </Section>
  );
}
