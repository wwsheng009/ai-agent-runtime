import { ArrowRightIcon, BookOpenIcon } from "lucide-react";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";

export function LandingFooter() {
  return (
    <footer className="container-shell mt-10 pb-10">
      <div className="rounded-[2rem] border border-[var(--border)] bg-[var(--panel-strong-bg)] px-6 py-10 text-center shadow-[0_20px_80px_rgba(0,0,0,0.18)]">
        <p className="font-serif text-2xl text-[var(--foreground)] sm:text-3xl">
          &quot;Keep the thread clear, the runtime visible, and the output reviewable.&quot;
        </p>
        <p className="mt-4 text-sm leading-7 text-[var(--muted-foreground)]">
          AI Agent Runtime gives teams a product-grade entry point into active
          work, with threads, runtime events, and artifacts kept close enough to
          support real review and handoff.
        </p>
        <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
          <Link to="/workspace">
            <Button variant="primary" size="md">
              Open the workspace
              <ArrowRightIcon size={16} />
            </Button>
          </Link>
          <a href="#product-highlights">
            <Button variant="secondary" size="md">
              <BookOpenIcon size={16} />
              View product highlights
            </Button>
          </a>
        </div>
      </div>
    </footer>
  );
}
