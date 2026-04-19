import { type ReactNode } from "react";

import { SettingsPanelCard } from "./settings-panel-card";

type SettingsSubsectionCardProps = {
  children?: ReactNode;
  description?: ReactNode;
  headerAside?: ReactNode;
  icon?: ReactNode;
  title?: ReactNode;
  bodyClassName?: string;
  className?: string;
  headerClassName?: string;
};

export function SettingsSubsectionCard({
  children,
  description,
  headerAside,
  icon,
  title,
  bodyClassName,
  className,
  headerClassName,
}: SettingsSubsectionCardProps) {
  return (
    <SettingsPanelCard
      className={className ?? "rounded-[0.8rem]"}
      title={title}
      icon={icon}
      description={description}
      descriptionClassName="mt-1 text-xs leading-5"
      headerAside={headerAside}
      bodyClassName={bodyClassName}
      headerClassName={headerClassName}
    >
      {children}
    </SettingsPanelCard>
  );
}
