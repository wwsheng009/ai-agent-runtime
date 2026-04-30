import {
  BlocksIcon,
  BrainCircuitIcon,
  FileJsonIcon,
  LayoutDashboardIcon,
  MessageSquareMoreIcon,
  RouteIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Section } from "@/components/landing/section";

export function WhatsNewSection() {
  const { t } = useTranslation("landing");
  const featureCards = [
    {
      description: t("whatsNew.cards.productShell.description"),
      icon: LayoutDashboardIcon,
      label: t("whatsNew.cards.productShell.label"),
      title: t("whatsNew.cards.productShell.title"),
    },
    {
      description: t("whatsNew.cards.workspaceEntry.description"),
      icon: RouteIcon,
      label: t("whatsNew.cards.workspaceEntry.label"),
      title: t("whatsNew.cards.workspaceEntry.title"),
    },
    {
      description: t("whatsNew.cards.runtimeSignal.description"),
      icon: MessageSquareMoreIcon,
      label: t("whatsNew.cards.runtimeSignal.label"),
      title: t("whatsNew.cards.runtimeSignal.title"),
    },
    {
      description: t("whatsNew.cards.artifactEvidence.description"),
      icon: FileJsonIcon,
      label: t("whatsNew.cards.artifactEvidence.label"),
      title: t("whatsNew.cards.artifactEvidence.title"),
    },
    {
      description: t("whatsNew.cards.conversation.description"),
      icon: BrainCircuitIcon,
      label: t("whatsNew.cards.conversation.label"),
      title: t("whatsNew.cards.conversation.title"),
    },
    {
      description: t("whatsNew.cards.extension.description"),
      icon: BlocksIcon,
      label: t("whatsNew.cards.extension.label"),
      title: t("whatsNew.cards.extension.title"),
    },
  ];

  return (
    <Section
      eyebrow={t("whatsNew.eyebrow")}
      title={t("whatsNew.title")}
      subtitle={t("whatsNew.subtitle")}
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
