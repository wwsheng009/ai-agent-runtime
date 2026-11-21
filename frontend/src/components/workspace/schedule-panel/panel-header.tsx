// Schedule 面板头部：标题/计数、新建、刷新与全局自动化入口。

import {
  CalendarClockIcon,
  ExternalLinkIcon,
  PlusIcon,
  RefreshCwIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { buildRuntimePagePath } from "@/api/runtime/shared";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type SchedulePanelHeaderProps = {
  createOpen: boolean;
  loading: boolean;
  onCreate: () => void;
  onRefresh: () => void;
  showGlobalAutomationLink: boolean;
  taskCount: number;
};

export function SchedulePanelHeader({
  createOpen,
  loading,
  onCreate,
  onRefresh,
  showGlobalAutomationLink,
  taskCount,
}: SchedulePanelHeaderProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="flex items-center justify-between">
      <div className="flex items-center gap-2">
        <CalendarClockIcon className="size-4" />
        <h2 className="text-sm font-semibold">
          {t("panels.schedule.title")}
        </h2>
        {taskCount > 0 && (
          <Badge>{taskCount}</Badge>
        )}
      </div>
      <div className="flex items-center gap-1">
        <Button
          aria-expanded={createOpen}
          aria-haspopup="dialog"
          onClick={onCreate}
          size="sm"
          variant="secondary"
        >
          <PlusIcon aria-hidden="true" className="size-4" />
          {t("panels.schedule.create")}
        </Button>
        <Button
          aria-label={t("panels.schedule.refresh")}
          disabled={loading}
          onClick={onRefresh}
          size="icon"
          variant="ghost"
        >
          <RefreshCwIcon
            aria-hidden="true"
            className={cn("size-4", loading && "animate-spin")}
          />
        </Button>
        {showGlobalAutomationLink ? (
          <a
            aria-label={t("panels.schedule.openGlobalAutomation")}
            className={cn(buttonVariants({ variant: "ghost", size: "icon" }))}
            data-testid="schedule-global-automation"
            href={buildRuntimePagePath("/automation")}
            title={t("panels.schedule.openGlobalAutomation")}
          >
            <ExternalLinkIcon aria-hidden="true" className="size-4" />
          </a>
        ) : null}
      </div>
    </div>
  );
}