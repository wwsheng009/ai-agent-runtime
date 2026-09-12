import { type ReactNode } from "react";

import { PanelIcon } from "@/components/ui/panel-icon";

type SettingsPanelIconProps = {
  children: ReactNode;
  className?: string;
};

export function SettingsPanelIcon({
  children,
  className,
}: SettingsPanelIconProps) {
  return <PanelIcon className={className}>{children}</PanelIcon>;
}
