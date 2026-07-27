import { ArrowRightIcon, BookOpenIcon, SparklesIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function LandingHeader() {
  const { t } = useTranslation("landing");

  return (
    <header className="fixed top-0 right-0 left-0 z-40 border-b border-[var(--border)] bg-[var(--landing-header-bg)] backdrop-blur-xl">
      <div className="container-shell flex h-16 items-center justify-between gap-2 sm:gap-4">
        <Link to="/" className="flex min-w-0 items-center gap-2 sm:gap-3" aria-label={t("header.productName")}>
          <span className="grid size-9 shrink-0 place-items-center rounded-xl border border-[var(--border)] bg-[var(--surface-soft)] text-sm font-semibold text-[var(--accent-primary)] sm:size-10 sm:rounded-2xl">
            AR
          </span>
          <div className="min-w-0">
            <div className="hidden text-sm uppercase tracking-[0.2em] text-[var(--muted-foreground)] sm:block">
              {t("header.productLabel")}
            </div>
            <div className="truncate text-sm font-semibold tracking-[-0.02em] sm:text-base">
              {t("header.productName")}
            </div>
          </div>
        </Link>
        <nav className="flex shrink-0 items-center gap-1 sm:gap-3" aria-label={t("header.productLabel")}>
          <a
            href="#product-highlights"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            aria-label={t("header.productTour")}
            title={t("header.productTour")}
          >
            <BookOpenIcon size={16} />
            <span className="hidden md:inline">{t("header.productTour")}</span>
          </a>
          <Link
            to="/workspace"
            className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
            aria-label={t("header.openWorkspace")}
          >
            <SparklesIcon size={16} />
            <span className="hidden sm:inline">{t("header.openWorkspace")}</span>
            <ArrowRightIcon size={16} className="hidden md:block" />
          </Link>
        </nav>
      </div>
    </header>
  );
}
