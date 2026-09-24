// Profiles 编辑器原子控件：9 张卡片共用的受控输入与只读展示件。
//
// 只做「受控值 → onChange」这一件事：草稿状态、校验、保存都留在
// profile-editor-dialog，卡片层不持有状态，便于 vitest 直接静态渲染断言。
//
// 只读件（生效清单 / 已连接 server / 可用 agent）统一走 ProfileReadonlyChips：
// 它们是后端 resolved view 的投影，前端不得写回，因此没有 onChange 入口。

import { type ReactNode } from "react";

import { Badge } from "@/components/ui/badge";
import { Select, type SelectOption } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import { ConfigFormField } from "../../config-form-field";
import { editorControlClassName, editorToggleRowClassName } from "../../editor-control-class";
import { SettingsBadgeList } from "../../settings-badge-list";

type ProfileFieldProps = {
  children: ReactNode;
  description?: string;
  label: string;
};

/** 与 ConfigFormField 同形，但供非 labelable 组合控件（自定义 Select / 只读块）使用。 */
export function ProfileField({ children, description, label }: ProfileFieldProps) {
  return (
    <ConfigFormField description={description} label={label}>
      {children}
    </ConfigFormField>
  );
}

export function ProfileTextField({
  description,
  disabled = false,
  label,
  maxLength,
  onChange,
  placeholder,
  value,
}: {
  description?: string;
  disabled?: boolean;
  label: string;
  maxLength?: number;
  onChange: (value: string) => void;
  placeholder?: string;
  value: string;
}) {
  return (
    <ConfigFormField description={description} label={label}>
      <input
        className={cn(editorControlClassName, "disabled:opacity-60")}
        disabled={disabled}
        maxLength={maxLength}
        placeholder={placeholder}
        type="text"
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
    </ConfigFormField>
  );
}

export function ProfileTextAreaField({
  description,
  disabled = false,
  label,
  onChange,
  rows = 4,
  value,
}: {
  description?: string;
  disabled?: boolean;
  label: string;
  onChange: (value: string) => void;
  rows?: number;
  value: string;
}) {
  return (
    <ConfigFormField description={description} label={label}>
      <textarea
        className={cn(editorControlClassName, "resize-y font-mono leading-6 disabled:opacity-60")}
        disabled={disabled}
        rows={rows}
        spellCheck={false}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
    </ConfigFormField>
  );
}

export function ProfileSelectField({
  description,
  disabled = false,
  label,
  onChange,
  options,
  placeholder,
  value,
}: {
  description?: string;
  disabled?: boolean;
  label: string;
  onChange: (value: string) => void;
  options: readonly SelectOption[];
  placeholder?: string;
  value: string;
}) {
  return (
    <ConfigFormField description={description} label={label}>
      <Select
        ariaLabel={label}
        className="w-full"
        disabled={disabled}
        options={options}
        placeholder={placeholder}
        triggerClassName={editorControlClassName}
        value={value}
        onChange={onChange}
      />
    </ConfigFormField>
  );
}

/** 只读徽标清单：values 为空时渲染 emptyLabel，避免出现空卡片。 */
export function ProfileReadonlyChips({
  description,
  emptyLabel,
  label,
  toneClassName,
  values,
}: {
  description?: string;
  emptyLabel: string;
  label: string;
  toneClassName?: string;
  values: readonly string[];
}) {
  return (
    <ProfileField description={description} label={label}>
      {values.length > 0 ? (
        <SettingsBadgeList compact>
          {values.map((item) => (
            <Badge key={item} className={cn("normal-case", toneClassName)}>
              {item}
            </Badge>
          ))}
        </SettingsBadgeList>
      ) : (
        <div className="text-xs leading-5 text-muted-foreground">{emptyLabel}</div>
      )}
    </ProfileField>
  );
}

/** 只读文本行（path / ref 等）：可选中复制，但不提供编辑入口。 */
export function ProfileReadonlyText({
  description,
  label,
  value,
}: {
  description?: string;
  label: string;
  value: string;
}) {
  return (
    <ProfileField description={description} label={label}>
      <div className="select-all break-all font-mono text-xs leading-6 text-muted-foreground">
        {value || "-"}
      </div>
    </ProfileField>
  );
}

/** 布尔开关行（目前只有「强制删除」这类对话框选项使用）。 */
export function ProfileToggleRow({
  checked,
  description,
  disabled = false,
  label,
  onChange,
}: {
  checked: boolean;
  description?: string;
  disabled?: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className={cn(editorToggleRowClassName, disabled ? "opacity-60" : undefined)}>
      <span className="min-w-0">
        <span className="block text-sm text-foreground">{label}</span>
        {description ? (
          <span className="mt-0.5 block text-xs leading-5 text-muted-foreground">
            {description}
          </span>
        ) : null}
      </span>
      <input
        checked={checked}
        disabled={disabled}
        type="checkbox"
        onChange={(event) => {
          onChange(event.target.checked);
        }}
      />
    </label>
  );
}

/** 卡片头：图标 + 标题 + 说明，9 张卡片共用同一形制。 */
export function ProfileCardHeader({
  description,
  icon,
  title,
}: {
  description: string;
  icon: ReactNode;
  title: string;
}) {
  return (
    <div className="flex items-start gap-2">
      <span className="mt-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
        {icon}
      </span>
      <div className="min-w-0">
        <div className="text-sm font-semibold text-foreground">{title}</div>
        <p className="mt-1 max-w-[46rem] text-xs leading-5 text-muted-foreground">
          {description}
        </p>
      </div>
    </div>
  );
}
