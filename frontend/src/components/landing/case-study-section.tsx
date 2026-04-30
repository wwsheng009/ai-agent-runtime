import {
  ArrowUpRightIcon,
  BotIcon,
  FileSearchIcon,
  LayoutTemplateIcon,
  RadioTowerIcon,
  UsersIcon,
} from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { buttonVariants } from "@/components/ui/button";
import { CardTitle } from "@/components/ui/card";

import { Section } from "@/components/landing/section";
import { cn } from "@/lib/utils";

export function CaseStudySection() {
  const { t } = useTranslation("landing");
  const caseStudies = [
    {
      accent: "from-[#7dcfff]/35 via-[#0f172a] to-transparent",
      description: t("caseStudy.cards.liveExecution.description"),
      icon: RadioTowerIcon,
      label: t("caseStudy.cards.liveExecution.label"),
      title: t("caseStudy.cards.liveExecution.title"),
    },
    {
      accent: "from-[#f0c77b]/35 via-[#201812] to-transparent",
      description: t("caseStudy.cards.teamCoordination.description"),
      icon: UsersIcon,
      label: t("caseStudy.cards.teamCoordination.label"),
      title: t("caseStudy.cards.teamCoordination.title"),
    },
    {
      accent: "from-[#8fd0c6]/28 via-[#091717] to-transparent",
      description: t("caseStudy.cards.artifactDetail.description"),
      icon: LayoutTemplateIcon,
      label: t("caseStudy.cards.artifactDetail.label"),
      title: t("caseStudy.cards.artifactDetail.title"),
    },
    {
      accent: "from-[#8fd0c6]/22 via-[#0d1117] to-transparent",
      description: t("caseStudy.cards.agentReasoning.description"),
      icon: BotIcon,
      label: t("caseStudy.cards.agentReasoning.label"),
      title: t("caseStudy.cards.agentReasoning.title"),
    },
    {
      accent: "from-[#f0c77b]/25 via-[#160f0f] to-transparent",
      description: t("caseStudy.cards.operationalClarity.description"),
      icon: FileSearchIcon,
      label: t("caseStudy.cards.operationalClarity.label"),
      title: t("caseStudy.cards.operationalClarity.title"),
    },
    {
      accent: "from-[#f97316]/24 via-[#1a1010] to-transparent",
      description: t("caseStudy.cards.productExperience.description"),
      icon: ArrowUpRightIcon,
      label: t("caseStudy.cards.productExperience.label"),
      title: t("caseStudy.cards.productExperience.title"),
    },
  ];

  return (
    <Section
      className="pt-0"
      eyebrow={t("caseStudy.eyebrow")}
      title={t("caseStudy.title")}
      subtitle={t("caseStudy.subtitle")}
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
                  {t("caseStudy.exploreInWorkspace")}
                </span>
              </div>
            </div>
          </Link>
        ))}
      </div>
    </Section>
  );
}
