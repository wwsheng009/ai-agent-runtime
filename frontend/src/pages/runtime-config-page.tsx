import { ArrowLeftIcon, DatabaseIcon, TerminalSquareIcon } from "lucide-react";
import { Link } from "react-router-dom";

import { BackendConfigSettingsPage } from "@/components/workspace/settings/backend-config-settings-page";
import { buttonVariants } from "@/components/ui/button-variants";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function RuntimeConfigPage() {
  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-[var(--foreground)]">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3">
        <header className="surface-panel relative overflow-hidden rounded-[0.95rem] px-3.5 py-3">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_22%)]" />
          <div className="relative flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]">
                  <DatabaseIcon size={13} />
                  Runtime config
                </Badge>
                <Badge>独立页面</Badge>
              </div>
              <div>
                <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                  后端配置工作台
                </h1>
                <p className="mt-1 max-w-3xl text-sm leading-6 text-[var(--muted-foreground)]">
                  独立处理 runtime 后端配置，并为 provider 提供专门入口。
                </p>
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Link
                to="/workspace/chats/new"
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                <ArrowLeftIcon size={14} />
                返回工作台
              </Link>
              <Link
                to="/logs"
                className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
              >
                <TerminalSquareIcon size={14} />
                日志
              </Link>
            </div>
          </div>
        </header>

        <section className="surface-panel rounded-[0.95rem] px-3 py-3.5 sm:px-4">
          <BackendConfigSettingsPage />
        </section>
      </div>
    </div>
  );
}
