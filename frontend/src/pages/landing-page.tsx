import { lazy, startTransition, Suspense, useEffect, useRef, useState } from "react";

import { LandingHeader } from "@/components/landing/landing-header";
import { Hero } from "@/components/landing/hero";
import "@/styles/landing.css";

const LandingDeferredSections = lazy(() =>
  import("@/components/landing/landing-deferred-sections").then((module) => ({
    default: module.LandingDeferredSections,
  })),
);

const LANDING_DEFERRED_ROOT_MARGIN = "640px 0px";

export function LandingPage() {
  const [deferredSectionsReady, setDeferredSectionsReady] = useState(false);
  const deferredSectionsTriggerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (deferredSectionsReady) {
      return;
    }

    const trigger = deferredSectionsTriggerRef.current;
    if (!trigger || typeof IntersectionObserver !== "function") {
      startTransition(() => {
        setDeferredSectionsReady(true);
      });
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) {
          return;
        }

        observer.disconnect();
        startTransition(() => {
          setDeferredSectionsReady(true);
        });
      },
      { rootMargin: LANDING_DEFERRED_ROOT_MARGIN },
    );

    observer.observe(trigger);
    return () => {
      observer.disconnect();
    };
  }, [deferredSectionsReady]);

  return (
    <div className="min-h-screen overflow-x-hidden pb-10">
      <LandingHeader />
      <main className="flex w-full flex-col">
        <Hero />
        <div ref={deferredSectionsTriggerRef} aria-hidden="true" className="h-px w-full" />
        {deferredSectionsReady ? (
          <Suspense fallback={<LandingDeferredSectionsFallback />}>
            <LandingDeferredSections />
          </Suspense>
        ) : (
          <LandingDeferredSectionsFallback />
        )}
      </main>
    </div>
  );
}

function LandingDeferredSectionsFallback() {
  return (
    <section aria-hidden="true" className="container-shell py-10 sm:py-14">
      <div className="rounded-[2.25rem] border border-[var(--border)] bg-[var(--section-panel-bg)] px-6 py-10 shadow-[0_24px_90px_rgba(0,0,0,0.16)] backdrop-blur-xl sm:px-10 sm:py-12">
        <div className="grid gap-4 md:grid-cols-3">
          <div className="h-40 rounded-[1.7rem] border border-[var(--border)] bg-[var(--surface-soft)]" />
          <div className="h-40 rounded-[1.7rem] border border-[var(--border)] bg-[var(--surface-soft)]" />
          <div className="h-40 rounded-[1.7rem] border border-[var(--border)] bg-[var(--surface-soft)]" />
        </div>
      </div>
    </section>
  );
}
