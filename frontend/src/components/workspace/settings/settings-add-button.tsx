import { PlusIcon } from "lucide-react";
import { type ReactNode } from "react";

import { Button, type ButtonProps } from "@/components/ui/button";

type SettingsAddButtonProps = Omit<ButtonProps, "children"> & {
  label: ReactNode;
  iconSize?: number;
};

export function SettingsAddButton({
  label,
  iconSize = 14,
  ...props
}: SettingsAddButtonProps) {
  return (
    <Button {...props}>
      <PlusIcon size={iconSize} />
      {label}
    </Button>
  );
}
