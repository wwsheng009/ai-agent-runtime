import {
  BookOpenIcon,
  GitBranchPlusIcon,
  LayoutPanelLeftIcon,
  SparklesIcon,
} from "lucide-react";
import { Link } from "react-router-dom";

import { Section } from "@/components/landing/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useTranslation } from "react-i18next";

export function CommunitySection() {
  const { t } = useTranslation("landing");
  const productPillars = [
    {
      summary: t("community.pillars.focusedWork.summary"),
      title: t("community.pillars.focusedWork.title"),
    },
    {
      summary: t("community.pillars.sharedVisibility.summary"),
      title: t("community.pillars.sharedVisibility.title"),
    },
    {
      summary: t("community.pillars.inspectableResults.summary"),
      title: t("community.pillars.inspectableResults.title"),
    },
  ];

  return (
    <Section
      eyebrow={t("community.eyebrow")}
      title={t("community.title")}
      subtitle={t("community.subtitle")}
    >
      <div className="grid gap-4 lg:grid-cols-[1.1fr_0.9fr]">
        <div className="rounded-[1.7rem] border border-white/8 bg-black/20 p-5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{t("community.pillarsBadge")}</Badge>
            <Badge className="border-transparent bg-white/10 text-[var(--foreground)]">
              {t("community.layersBadge")}
            </Badge>
          </div>
          <div className="mt-5 space-y-3">
            {productPillars.map((item) => (
              <div
                key={item.title}
                className="rounded-[1.15rem] border border-white/8 bg-white/4 px-4 py-4"
              >
                <div className="text-sm font-semibold text-[var(--foreground)]">
                  {item.title}
                </div>
                <p className="mt-2 text-sm leading-7 text-[var(--muted-foreground)]">
                  {item.summary}
                </p>
              </div>
            ))}
          </div>
        </div>

        <div className="rounded-[1.7rem] border border-[#8fd0c6]/14 bg-[linear-gradient(180deg,rgba(143,208,198,0.09),rgba(255,255,255,0.03))] p-5">
          <div className="inline-flex items-center gap-2 rounded-full border border-[#8fd0c6]/20 bg-[#8fd0c6]/10 px-3 py-1 app-text-11 uppercase tracking-[0.2em] text-[#8fd0c6]">
            <GitBranchPlusIcon size={14} />
            {t("community.readyBadge")}
          </div>
          <h3 className="mt-5 font-serif text-3xl tracking-[-0.03em]">
            {t("community.readyTitle")}
          </h3>
          <div className="mt-6 space-y-3 text-sm leading-7 text-[var(--muted-foreground)]">
            <div className="flex gap-3">
              <LayoutPanelLeftIcon size={18} className="mt-1 shrink-0 text-[#f0c77b]" />
              <span>{t("community.readyPoint1")}</span>
            </div>
            <div className="flex gap-3">
              <BookOpenIcon size={18} className="mt-1 shrink-0 text-[#f0c77b]" />
              <span>{t("community.readyPoint2")}</span>
            </div>
          </div>
          <div className="mt-6 flex flex-wrap gap-3">
            <Link to="/workspace">
              <Button variant="primary" size="md">
                <SparklesIcon size={16} />
                {t("community.launchWorkspace")}
              </Button>
            </Link>
            <a href="#product-highlights">
              <Button variant="secondary" size="md">
                {t("community.browseHighlights")}
              </Button>
            </a>
          </div>
        </div>
      </div>
    </Section>
  );
}
