import { type ReactNode } from "react";

import { SectionTitleRow } from "@/components/ui/section-header";
import { SurfaceCard } from "@/components/ui/surface-card";
import { cn } from "@/lib/utils";

type SettingsFieldCardProps = {
  children: ReactNode;
  title: ReactNode;
  bodyClassName?: string;
  className?: string;
  icon?: ReactNode;
  titleClassName?: string;
};

export function SettingsFieldCard({
  children,
  title,
  bodyClassName,
  className,
  icon,
  titleClassName,
}: SettingsFieldCardProps) {
  return (
    <SurfaceCard className={className}>
      <SectionTitleRow className={titleClassName}>
        {icon}
        {title}
      </SectionTitleRow>
      <div className={cn("mt-2.5", bodyClassName)}>{children}</div>
    </SurfaceCard>
  );
}
