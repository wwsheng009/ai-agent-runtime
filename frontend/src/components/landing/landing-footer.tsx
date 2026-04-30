import { ArrowRightIcon, BookOpenIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

export function LandingFooter() {
  const { t } = useTranslation("landing");

  return (
    <footer className="container-shell mt-10 pb-10">
      <div className="rounded-[2rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] px-6 py-10 text-center shadow-[0_20px_80px_rgba(0,0,0,0.18)]">
        <p className="font-serif text-2xl text-[var(--foreground)] sm:text-3xl">
          {t("footer.quote")}
        </p>
        <p className="mt-4 text-sm leading-7 text-[var(--muted-foreground)]">
          {t("footer.body")}
        </p>
        <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
          <Link to="/workspace">
            <Button variant="primary" size="md">
              {t("footer.openWorkspace")}
              <ArrowRightIcon size={16} />
            </Button>
          </Link>
          <a href="#product-highlights">
            <Button variant="secondary" size="md">
              <BookOpenIcon size={16} />
              {t("footer.viewProductHighlights")}
            </Button>
          </a>
        </div>
      </div>
    </footer>
  );
}
