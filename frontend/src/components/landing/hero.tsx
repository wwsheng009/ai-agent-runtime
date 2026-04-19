import {
  ArrowRightIcon,
  CompassIcon,
  GitBranchPlusIcon,
  SparklesIcon,
} from "lucide-react";
import { useEffect, useEffectEvent, useState } from "react";
import { Link } from "react-router-dom";

import { buttonVariants } from "@/components/ui/button";
import { Card, CardDescription, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const rotatingWords = [
  "Deep Research",
  "Agent Workspaces",
  "Artifact Reviews",
  "Runtime Teams",
];

export function Hero() {
  const [index, setIndex] = useState(0);
  const rotateWord = useEffectEvent(() => {
    setIndex((current) => (current + 1) % rotatingWords.length);
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
            Browser-native AI workspace for research, execution, and review
          </div>
          <h1 className="section-title mt-7 max-w-4xl">
            Research, orchestrate, and ship{" "}
            <span className="text-[var(--accent-primary)]">{rotatingWords[index]}</span>
            <br />
            from one browser-native workspace.
          </h1>
          <p className="mt-6 max-w-3xl text-lg leading-8 text-[var(--muted-foreground)] md:text-xl">
            AI Agent Runtime brings live threads, runtime events, artifact
            evidence, and teammate coordination into a single product surface.
            Start from the landing page, enter the workspace, and keep the full
            path from request to verified output visible in one place.
          </p>
          <div className="mt-8 flex flex-wrap items-center gap-3">
            <Link
              to="/workspace"
              className={cn(buttonVariants({ variant: "primary", size: "lg" }))}
            >
              Enter workspace
              <ArrowRightIcon size={18} />
            </Link>
            <a
              href="#product-highlights"
              className={cn(buttonVariants({ variant: "secondary", size: "lg" }))}
            >
              See product highlights
            </a>
          </div>
          <div className="mt-10 grid max-w-4xl gap-4 sm:grid-cols-3">
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-primary)]">
                <CompassIcon size={16} />
                Unified flow
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                Move from prompt to action to review without jumping across
                disconnected tools or hidden execution surfaces.
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-secondary)]">
                <GitBranchPlusIcon size={16} />
                Team-ready workspace
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                Keep thread context, runtime teams, and operational detail in
                the same shell so handoffs stay readable.
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--surface-soft)] p-4">
              <div className="flex items-center gap-2 text-sm font-semibold text-[var(--accent-primary)]">
                <ArrowRightIcon size={16} />
                Verifiable output
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                Inspect artifacts, stream runtime events, and keep the evidence
                next to the work that produced it.
              </p>
            </div>
          </div>
        </div>

        <Card className="relative overflow-hidden rounded-[2rem] border-[var(--border)] bg-[var(--panel-strong-bg)] p-0">
          <div className="border-b border-[var(--border)] px-6 py-5">
            <div className="eyebrow border-[var(--border)] bg-[var(--surface-soft)] text-[var(--foreground)]">
              Product snapshot
            </div>
            <CardTitle className="mt-4 text-2xl">One shell, three visible layers</CardTitle>
            <CardDescription className="mt-2">
              Keep discovery, active work, and runtime evidence legible at the
              same time: the product story up front, the active thread in the
              middle, and the operational detail around it.
            </CardDescription>
          </div>
          <div className="grid gap-4 p-6">
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <SparklesIcon className="text-[var(--accent-primary)]" size={18} />
                <div className="text-sm font-semibold">Product site</div>
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                A clear product narrative explains what the workspace does,
                where it fits, and why teams can trust the output.
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <CompassIcon className="text-[var(--accent-secondary)]" size={18} />
                <div className="text-sm font-semibold">Workspace entry</div>
              </div>
              <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                Start with <code className="app-inline-mono">/workspace</code> and land directly in an active
                thread, so the app feels immediate instead of route-driven.
              </p>
            </div>
            <div className="rounded-[1.5rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] p-4">
              <div className="flex items-center gap-3">
                <GitBranchPlusIcon className="text-[var(--accent-primary)]" size={18} />
                <div className="text-sm font-semibold">Runtime evidence</div>
              </div>
              <div className="app-terminal-copy mt-3 space-y-2 text-[var(--muted-foreground)]">
                <div>Chat turns and replies stay attached to the thread.</div>
                <div>History sync keeps the workspace aligned with the session.</div>
                <div>Runtime streams expose tool calls, routes, and artifacts.</div>
              </div>
            </div>
          </div>
        </Card>
      </div>
    </section>
  );
}
