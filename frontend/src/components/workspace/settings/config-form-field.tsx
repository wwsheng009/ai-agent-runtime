import { InfoIcon } from "lucide-react";
import {
  Children,
  cloneElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useId,
} from "react";

type ConfigFormFieldProps = {
  children: ReactNode;
  description?: string;
  label: string;
};

type LabelableControlElement = ReactElement<{
  id?: string;
}>;

const LABELABLE_CONTROL_TYPES = new Set([
  "button",
  "input",
  "meter",
  "output",
  "progress",
  "select",
  "textarea",
]);

function isLabelableControl(node: ReactNode): node is LabelableControlElement {
  return (
    Children.count(node) === 1 &&
    isValidElement<{ id?: string }>(node) &&
    typeof node.type === "string" &&
    LABELABLE_CONTROL_TYPES.has(node.type)
  );
}

export function ConfigFormField({
  children,
  description,
  label,
}: ConfigFormFieldProps) {
  const generatedFieldId = useId();
  const labelableChild = isLabelableControl(children) ? children : null;
  const fieldId =
    labelableChild && typeof labelableChild.props.id === "string" && labelableChild.props.id.length > 0
      ? labelableChild.props.id
      : labelableChild
        ? generatedFieldId
        : undefined;
  const renderedChildren =
    labelableChild && fieldId !== labelableChild.props.id
      ? cloneElement(labelableChild, { id: fieldId })
      : children;

  return (
    <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
      <div className="flex items-center gap-2">
        {fieldId ? (
          <label htmlFor={fieldId} className="text-sm font-semibold text-[var(--foreground)]">
            {label}
          </label>
        ) : (
          <div className="text-sm font-semibold text-[var(--foreground)]">{label}</div>
        )}
        {description ? (
          <span
            title={description}
            className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
          >
            <InfoIcon size={12} />
          </span>
        ) : null}
      </div>
      <div className="mt-2">{renderedChildren}</div>
    </div>
  );
}
