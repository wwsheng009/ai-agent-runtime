import { type ReactNode } from "react";

import {
  SectionBody,
  SectionDescriptionText,
  SectionTitleRow,
} from "@/components/ui/section-header";
import { SurfaceCard } from "@/components/ui/surface-card";

type SettingsInfoCardProps = {
  children?: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  title?: ReactNode;
  className?: string;
  contentClassName?: string;
  descriptionClassName?: string;
  size?: "compact" | "default";
  tone?: "solid" | "softer";
};

export function SettingsInfoCard({
  children,
  description,
  icon,
  title,
  className,
  contentClassName,
  descriptionClassName,
  size = "default",
  tone = "solid",
}: SettingsInfoCardProps) {
  const hasHeader = icon !== undefined || title !== undefined;
  const hasDescription = description !== undefined;

  return (
    <SurfaceCard
      surface={tone}
      radius={tone === "softer" ? "xl" : "lg"}
      density={size === "compact" ? "compact" : "default"}
      className={className}
    >
      {hasHeader ? (
        <SectionTitleRow>
          {icon}
          {title}
        </SectionTitleRow>
      ) : null}
      {hasDescription ? (
        <SectionDescriptionText
          gapClassName={hasHeader ? "mt-3" : undefined}
          className={descriptionClassName}
        >
          {description}
        </SectionDescriptionText>
      ) : null}
      {children ? (
        <SectionBody
          spaced={hasHeader || hasDescription}
          className={contentClassName}
        >
          {children}
        </SectionBody>
      ) : null}
    </SurfaceCard>
  );
}
