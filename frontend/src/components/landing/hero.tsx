import {
  ArrowRightIcon,
  CompassIcon,
  GitBranchPlusIcon,
  SparklesIcon,
} from "lucide-react";
import { useEffect, useEffectEvent, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { buttonVariants } from "@/components/ui/button";
import { Card, CardDescription, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export function Hero() {
  const { t } = useTranslation("landing");
  const [index, setIndex] = useState(0);
  const rotatingWords = [
    t("hero.rotatingWord1"),
    t("hero.rotatingWord2"),
    t("hero.rotatingWord3"),
    t("hero.rotatingWord4"),
  ];
  const rotateWord = useEffectEvent(() => {
    setIndex((current) => (current + 1) % 4);
  });

  useEffect(() => {
    const timer = window.setInterval(() => {
      rotateWord();
    }, 1800);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <section className="relative overflow-hidden pt-16">
      <div className="hero-grid" />
      <div className="hero-glow hero-glow-gold left-[6%] top-[5rem]" />
      <div className="hero-glow hero-glow-violet bottom-[4rem] right-[8%]" />
      <div className="absolute inset-0 bg-[var(--hero-overlay-bg)]" />
      <div className="container-shell relative grid min-h-[calc(100vh-4rem)] items-center gap-10 py-16 lg:grid-cols-[minmax(0,1fr)_26rem] lg:py-24">
        <div className="max-w-4xl animate-[var(--animate-fade-up)]">
          <div className="eyebrow">
            <SparklesIcon size={14} />
            {t("hero.eyebrow")}
          </div>
          <h1 className="section-title mt-7 max-w-4xl">
            {t("hero.titlePrefix")}{" "}
            <span className="text-[var(--accent-primary)]">
              {rotatingWords[index]}
            </span>
            <br />
            {t("hero.titleSuffix")}
          </h1>
          <p className="mt-6 max-w-3xl text-lg leading-8 text-[var(--muted-foreground)] md:text-xl">
            {t("hero.body")}
          </p>
          <div className="mt-8 flex flex-wrap items-center gap-3">
            <Link
              to="/workspace"
              className={cn(buttonVariants({ variant: "primary", size: "lg" }))}
            >
              {t("hero.primaryCta")}
              <ArrowRightIcon size={18} />
            </Link>
            <a
              href="#product-highlights"
              className={cn(buttonVariants({ variant: "secondary", size: "lg" }))}
            >
              {t("hero.secondaryCta")}
            </a>
          </div>
          <div className="mt-10 grid max-w-4xl gap-4 sm:grid-cols-3">
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-primary)]">
                <CompassIcon size={16} />
                {t("hero.unifiedFlowTitle")}
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {t("hero.unifiedFlowBody")}
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-secondary)]">
                <GitBranchPlusIcon size={16} />
                {t("hero.teamReadyTitle")}
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {t("hero.teamReadyBody")}
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-primary)]">
                <ArrowRightIcon size={16} />
                {t("hero.verifiableOutputTitle")}
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {t("hero.verifiableOutputBody")}
              </p>
            </div>
          </div>
        </div>

        <Card className="relative overflow-hidden rounded-[2rem] border-[var(--border)] bg-[var(--panel-strong-bg)] p-0">
          <div className="border-b border-[var(--border)] px-6 py-5">
            <div className="eyebrow border-[var(--border)] bg-[var(--surface-soft)] text-[var(--foreground)]">
              {t("hero.snapshotEyebrow")}
            </div>
            <CardTitle className="mt-4 text-2xl">{t("hero.snapshotTitle")}</CardTitle>
            <CardDescription className="mt-2">
              {t("hero.snapshotBody")}
            </CardDescription>
          </div>
          <div className="grid gap-4 p-6">
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <SparklesIcon className="text-[var(--accent-primary)]" size={18} />
                <div className="text-sm font-semibold">{t("hero.productSiteTitle")}</div>
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {t("hero.productSiteBody")}
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <CompassIcon className="text-[var(--accent-secondary)]" size={18} />
                <div className="text-sm font-semibold">{t("hero.workspaceEntryTitle")}</div>
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {t("hero.workspaceEntryBody")}
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <GitBranchPlusIcon className="text-[var(--accent-primary)]" size={18} />
                <div className="text-sm font-semibold">{t("hero.runtimeEvidenceTitle")}</div>
              </div>
              <div className="app-terminal-copy mt-3 space-y-2 text-[var(--muted-foreground)]">
                <div>{t("hero.runtimeEvidenceBody1")}</div>
                <div>{t("hero.runtimeEvidenceBody2")}</div>
                <div>{t("hero.runtimeEvidenceBody3")}</div>
              </div>
            </div>
          </div>
        </Card>
      </div>
    </section>
  );
}
