import { type ReactNode } from "react";

import {
  SectionAside,
  SectionBody,
  SectionDescriptionText,
  SectionHeaderMain,
  SectionHeaderRow,
  SectionTitleRow,
} from "@/components/ui/section-header";
import { SurfaceCard } from "@/components/ui/surface-card";
import { cn } from "@/lib/utils";

type SettingsPanelCardProps = {
  children?: ReactNode;
  description?: ReactNode;
  headerAside?: ReactNode;
  icon?: ReactNode;
  title?: ReactNode;
  asideClassName?: string;
  bodyClassName?: string;
  className?: string;
  descriptionClassName?: string;
  headerClassName?: string;
  tone?: "solid" | "softer";
};

export function SettingsPanelCard({
  children,
  description,
  headerAside,
  icon,
  title,
  asideClassName,
  bodyClassName,
  className,
  descriptionClassName,
  headerClassName,
  tone = "softer",
}: SettingsPanelCardProps) {
  const hasHeader = title !== undefined || icon !== undefined || description !== undefined;
  const hasTitleRow = title !== undefined || icon !== undefined;

  return (
    <SurfaceCard surface={tone} className={className}>
      {hasHeader ? (
        <SectionHeaderRow className={headerClassName}>
          <SectionHeaderMain>
            {hasTitleRow ? (
              <SectionTitleRow>
                {icon}
                {title}
              </SectionTitleRow>
            ) : null}
            {description !== undefined ? (
              <SectionDescriptionText
                gapClassName={hasTitleRow ? "mt-2" : undefined}
                className={descriptionClassName}
              >
                {description}
              </SectionDescriptionText>
            ) : null}
          </SectionHeaderMain>
          {headerAside ? (
            <SectionAside className={asideClassName}>{headerAside}</SectionAside>
          ) : null}
        </SectionHeaderRow>
      ) : null}
      {children ? (
        <SectionBody spaced={hasHeader} className={cn(bodyClassName)}>
          {children}
        </SectionBody>
      ) : null}
    </SurfaceCard>
  );
}
