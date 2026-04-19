import {
  BlocksIcon,
  BrainCircuitIcon,
  FileJsonIcon,
  LayoutDashboardIcon,
  MessageSquareMoreIcon,
  RouteIcon,
} from "lucide-react";

import { Section } from "@/components/landing/section";

const featureCards = [
  {
    description:
      "A clear sequence of hero, highlights, capabilities, runtime detail, and CTA helps explain the product before a user enters the workspace.",
    icon: LayoutDashboardIcon,
    label: "Product shell",
    title: "Structured for first-time understanding",
  },
  {
    description:
      "Primary calls to action now land on /workspace, reducing route noise and getting users into the active work surface faster.",
    icon: RouteIcon,
    label: "Workspace entry",
    title: "A cleaner path into active work",
  },
  {
    description:
      "Prompt submission, session history sync, and runtime streaming keep the thread current while work is still in flight.",
    icon: MessageSquareMoreIcon,
    label: "Runtime",
    title: "Live signals stay attached to the thread",
  },
  {
    description:
      "Planning, orchestration, route, subagent, and tool payloads stay attached back into inspectable artifacts and receipts.",
    icon: FileJsonIcon,
    label: "Artifacts",
    title: "Evidence remains inspectable",
  },
  {
    description:
      "The message surface keeps focusing on the selected thread instead of scattering execution state across disconnected screens.",
    icon: BrainCircuitIcon,
    label: "Conversation",
    title: "Thread-first workspace flow",
  },
  {
    description:
      "The structure leaves room for richer markdown, deeper artifact review, and more advanced team controls without changing the core flow.",
    icon: BlocksIcon,
    label: "Extension",
    title: "Ready to grow with the runtime",
  },
];

export function WhatsNewSection() {
  return (
    <Section
      eyebrow="Why it works"
      title="Everything needed to move from prompt to verified output"
      subtitle="The landing page now explains the product in user terms: how you enter the workspace, how execution stays visible, and how artifacts remain tied to the thread that produced them."
    >
      <div className="grid gap-4 lg:grid-cols-3">
        {featureCards.map((card, index) => (
          <div
            key={card.title}
            className={
              index === 0
                ? "rounded-[1.8rem] border border-white/8 bg-[linear-gradient(180deg,rgba(240,199,123,0.12),rgba(255,255,255,0.03))] p-6 lg:col-span-2"
                : index === 3
                  ? "rounded-[1.8rem] border border-white/8 bg-[linear-gradient(180deg,rgba(143,208,198,0.12),rgba(255,255,255,0.03))] p-6 lg:col-span-2"
                  : "rounded-[1.8rem] border border-white/8 bg-white/4 p-6"
            }
          >
            <div className="flex items-center gap-3 text-sm font-semibold">
              <card.icon size={18} className="text-[#f0c77b]" />
              {card.label}
            </div>
            <h3 className="mt-5 text-2xl font-semibold tracking-[-0.03em]">
              {card.title}
            </h3>
            <p className="mt-4 text-sm leading-7 text-[var(--muted-foreground)]">
              {card.description}
            </p>
          </div>
        ))}
      </div>
    </Section>
  );
}
