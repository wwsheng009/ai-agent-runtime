import { cn } from "@/lib/utils";

type SectionProps = {
  className?: string;
  contentClassName?: string;
  eyebrow?: React.ReactNode;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  children: React.ReactNode;
};

export function Section({
  className,
  contentClassName,
  eyebrow = "Section",
  title,
  subtitle,
  children,
}: SectionProps) {
  return (
    <section className={cn("container-shell py-10 sm:py-14", className)}>
      <div className="relative overflow-hidden rounded-[2.25rem] border border-[var(--border)] bg-[var(--section-panel-bg)] px-6 py-10 shadow-[0_24px_90px_rgba(0,0,0,0.16)] backdrop-blur-xl sm:px-10 sm:py-12">
        <div className="pointer-events-none absolute inset-x-10 top-0 h-px bg-gradient-to-r from-transparent via-[var(--section-panel-topline)] to-transparent" />
        <header className="mx-auto flex max-w-4xl flex-col items-center text-center">
          <div className="app-text-11 uppercase tracking-[0.22em] text-[var(--accent-secondary)]">
            {eyebrow}
          </div>
          <h2 className="mt-4 font-serif text-4xl tracking-[-0.04em] sm:text-5xl">
            {title}
          </h2>
          {subtitle ? (
            <div className="mt-5 max-w-3xl text-base leading-8 text-[var(--muted-foreground)]">
              {subtitle}
            </div>
          ) : null}
        </header>
        <div className={cn("mt-10", contentClassName)}>{children}</div>
      </div>
    </section>
  );
}
