import { type ReactNode } from "react";

import {
  surfaceCardVariants,
  type SurfaceCardVariantProps,
} from "@/components/ui/surface-card";
import { cn } from "@/lib/utils";

type SettingsNoticeTone = "warning" | "warning-soft" | "neutral" | "muted";

type SettingsNoticeCardProps = {
  children: ReactNode;
  className?: string;
  tone?: SettingsNoticeTone;
};

type NoticeStyle = {
  surface: NonNullable<SurfaceCardVariantProps["surface"]>;
  density: NonNullable<SurfaceCardVariantProps["density"]>;
  text: string;
};

const noticeStyles: Record<SettingsNoticeTone, NoticeStyle> = {
  "warning-soft": {
    surface: "warning-soft",
    density: "compact",
    text: "text-sm text-[var(--foreground)]",
  },
  warning: {
    surface: "warning",
    density: "compact",
    text: "text-sm text-[#f59e7d]",
  },
  muted: {
    surface: "solid",
    density: "tight",
    text: "text-xs leading-6 text-[var(--muted-foreground)]",
  },
  neutral: {
    surface: "solid",
    density: "compact",
    text: "text-sm text-[var(--foreground)]",
  },
};

export function SettingsNoticeCard({
  children,
  className,
  tone = "neutral",
}: SettingsNoticeCardProps) {
  const style = noticeStyles[tone];

  return (
    <div
      className={cn(
        surfaceCardVariants({
          radius: "sm",
          surface: style.surface,
          density: style.density,
        }),
        style.text,
        className,
      )}
    >
      {children}
    </div>
  );
}
