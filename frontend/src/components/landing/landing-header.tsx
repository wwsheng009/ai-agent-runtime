import { ArrowRightIcon, BookOpenIcon, SparklesIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function LandingHeader() {
  const { t } = useTranslation("landing");

  return (
    <header className="fixed top-0 right-0 left-0 z-40 border-b border-[var(--border)] bg-[var(--landing-header-bg)] backdrop-blur-xl">
      <div className="container-shell flex h-16 items-center justify-between gap-4">
        <Link to="/" className="flex items-center gap-3">
          <span className="grid size-10 place-items-center rounded-2xl border border-[var(--border)] bg-[var(--surface-soft)] text-sm font-semibold text-[var(--accent-primary)]">
            AR
          </span>
          <div>
            <div className="text-sm uppercase tracking-[0.2em] text-[var(--muted-foreground)]">
              {t("header.productLabel")}
            </div>
            <div className="text-base font-semibold tracking-[-0.02em]">
              {t("header.productName")}
            </div>
          </div>
        </Link>
        <div className="flex items-center gap-3">
          <a
            href="#product-highlights"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
          >
            <BookOpenIcon size={16} />
            {t("header.productTour")}
          </a>
          <Link
            to="/workspace"
            className={cn(buttonVariants({ variant: "primary", size: "sm" }))}
          >
            <SparklesIcon size={16} />
            {t("header.openWorkspace")}
            <ArrowRightIcon size={16} />
          </Link>
        </div>
      </div>
    </header>
  );
}
