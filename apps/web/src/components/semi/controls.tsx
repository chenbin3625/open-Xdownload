import React, { createContext, forwardRef, useContext, useId, useLayoutEffect, useState, useRef } from "react";
import SButton from "@douyinfe/semi-ui/lib/es/button";
import SCheckbox from "@douyinfe/semi-ui/lib/es/checkbox";
// 只取 Label：整个 Form 入口会连带 Upload/Cascader/Slider 等字段组件（约 300 KB）。
import FormLabel from "@douyinfe/semi-ui/lib/es/form/label";
import "@douyinfe/semi-foundation/lib/es/form/form.css";
import SInput from "@douyinfe/semi-ui/lib/es/input";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import SSelect from "@douyinfe/semi-ui/lib/es/select";
import SSwitch from "@douyinfe/semi-ui/lib/es/switch";
import STabs from "@douyinfe/semi-ui/lib/es/tabs";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import { Eye, EyeOff, X } from "lucide-react";
import { cn } from "../../lib/cn";
import { t } from "../../lib/i18n";

const FieldContext = createContext<string | undefined>(undefined);
export function useFieldId() { return useContext(FieldContext); }
export function Field({ label, tooltip, htmlFor, children, className }: { label: React.ReactNode; tooltip?: React.ReactNode; htmlFor?: string; children: React.ReactNode; className?: string }) {
  const generated = useId(); const id = htmlFor ?? generated;
  return <FieldContext.Provider value={id}><div className={cn("semi-field", className)}><div className="flex items-center gap-1"><FormLabel id={`${id}-label`} name={id} text={label} />{tooltip}</div>{children}</div></FieldContext.Provider>;
}

export interface InputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "prefix"> {
  size?: "sm" | "md"; prefix?: React.ReactNode; suffix?: React.ReactNode;
  allowClear?: boolean; onPressEnter?: (e: React.KeyboardEvent<HTMLInputElement>) => void;
  onClear?: () => void; containerClassName?: string;
  mode?: "password";
}
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ size, onChange, onPressEnter, containerClassName, className, id, allowClear, onClear, mode, suffix, ...props }, ref) {
  const fieldId = useFieldId();
  const [visible, setVisible] = useState(false);
  const [internalValue, setInternalValue] = useState(props.defaultValue ?? "");
  const value = props.value ?? internalValue;
  const generatedId = useId(); const inputId = id ?? fieldId ?? generatedId;
  const actions = <>{allowClear && value !== "" && !props.disabled && <SButton theme="borderless" type="tertiary" size="small" icon={<X size={14} />} aria-label={t("清空")} onClick={() => { setInternalValue(""); onClear?.(); onChange?.({ target: { value: "" } } as React.ChangeEvent<HTMLInputElement>); }} />}{mode === "password" && <SButton theme="borderless" type="tertiary" size="small" icon={visible ? <EyeOff size={14} /> : <Eye size={14} />} aria-label={t(visible ? "隐藏" : "显示")} aria-pressed={visible} onClick={() => setVisible(v => !v)} />}{suffix}</>;
  return <SInput {...props as React.ComponentProps<typeof SInput>} value={value as string | number} id={inputId} type={mode === "password" ? visible ? "text" : "password" : props.type} size={size === "sm" ? "small" : "default"} className={cn(containerClassName, className)} suffix={allowClear || mode || suffix ? actions : undefined} onChange={(v, event) => { setInternalValue(v); onChange?.(event); }} onEnterPress={e => { if (!e.nativeEvent.isComposing) onPressEnter?.(e); }} ref={ref} />;
});
export const PasswordInput = forwardRef<HTMLInputElement, InputProps>((props, ref) => <Input {...props} ref={ref} mode="password" />);
export interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> { containerClassName?: string }
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea({ onChange, containerClassName, className, id, ...props }, ref) {
  const fieldId = useFieldId();
  return <TextArea {...props as React.ComponentProps<typeof TextArea>} id={id ?? fieldId} className={cn(containerClassName, className)} onChange={(_, event) => onChange?.(event as unknown as React.ChangeEvent<HTMLTextAreaElement>)} ref={ref} />;
});
export interface NumberInputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "value" | "onChange"> { value: number | null; onChange: (v: number | null) => void; size?: "sm" | "md"; addonAfter?: React.ReactNode }
export const NumberInput = forwardRef<HTMLInputElement, NumberInputProps>(function NumberInput({ value, onChange, size, addonAfter, id, ...props }, ref) {
  const fieldId = useFieldId(); return <InputNumber {...props as React.ComponentProps<typeof InputNumber>} id={id ?? fieldId} value={value ?? ""} forwardedRef={ref as React.ComponentProps<typeof InputNumber>["forwardedRef"]} onChange={v => onChange(v === "" ? null : Number(v))} size={size === "sm" ? "small" : "default"} suffix={addonAfter} />;
});
export interface SelectOption { value: string; label: string }
interface SelectProps { value: string; onChange: (value: string) => void; options: SelectOption[]; placeholder?: string; size?: "sm" | "md"; className?: string; ariaLabel?: string; id?: string; disabled?: boolean; motion?: boolean }
export function Select({ onChange, options, size, ariaLabel, id, ...props }: SelectProps) {
  const fieldId = useFieldId(); const labelId = useId();
  return <>{ariaLabel && <span id={labelId} className="sr-only">{ariaLabel}</span>}<SSelect {...props} id={id ?? fieldId} aria-labelledby={ariaLabel ? labelId : fieldId ? `${fieldId}-label` : undefined} optionList={options.map(option => ({ ...option, showTick: false }))} onChange={value => onChange(String(value))} size={size === "sm" ? "small" : "default"} /></>;
}
export function SearchSelect({ searchPlaceholder, onChange, options, size, ariaLabel, ...props }: SelectProps & { searchPlaceholder?: string }) {
  const fieldId = useFieldId(); const labelId = useId();
  const select = useRef<SSelect>(null); const [open, setOpen] = useState(false);
  const listId = `${labelId}-listbox`;
  const searchLabel = [ariaLabel, searchPlaceholder ?? t("搜索...")].filter(Boolean).join(" ");
  useLayoutEffect(() => {
    if (!open) return;
    const observer = new MutationObserver(labelList);
    function labelList() {
      const list = select.current?.optionContainerEl.current?.querySelector('[role="listbox"]');
      if (list) { list.id = listId; select.current?.triggerRef.current?.setAttribute("aria-controls", listId); observer.disconnect(); }
    }
    observer.observe(document.body, { childList: true, subtree: true });
    labelList();
    return () => observer.disconnect();
  }, [open, listId]);
  return <>{ariaLabel && <span id={labelId} className="sr-only">{ariaLabel}</span>}<SSelect {...props} ref={select} id={props.id ?? fieldId} aria-labelledby={ariaLabel ? labelId : fieldId ? `${fieldId}-label` : undefined} filter searchPosition="dropdown" onDropdownVisibleChange={setOpen} inputProps={{ autoFocus: true, role: "combobox", "aria-label": searchLabel, "aria-expanded": open, "aria-controls": listId, "aria-autocomplete": "list", onKeyDownCapture: event => {
    if (event.key === "Escape" && !event.nativeEvent.isComposing) {
      event.preventDefault(); event.stopPropagation();
      select.current?.close(); select.current?.triggerRef.current?.focus();
    }
  } }} searchPlaceholder={searchPlaceholder ?? t("搜索...")} emptyContent={t("无匹配项")} optionList={options.map(option => ({ ...option, showTick: false }))} onChange={value => onChange(String(value))} size={size === "sm" ? "small" : "default"} /></>;
}
export function Switch({ ariaLabel, onChange, ...props }: { checked: boolean; onChange: (v: boolean) => void; loading?: boolean; disabled?: boolean; ariaLabel?: string }) { return <SSwitch {...props} disabled={props.disabled || props.loading} aria-label={ariaLabel} onChange={checked => onChange(checked)} />; }
export function Checkbox({ onChange, label, ariaLabel, ...props }: { checked: boolean; onChange: (v: boolean) => void; label?: React.ReactNode; ariaLabel?: string; id?: string }) { return <SCheckbox {...props} aria-label={ariaLabel} onChange={e => onChange(e.target.checked === true)}>{label}</SCheckbox>; }
export interface TabItem { key: string; label: React.ReactNode; children?: React.ReactNode }
export function Tabs({ value, onChange, items, className }: { value: string; onChange: (v: string) => void; items: TabItem[]; className?: string }) { return <STabs activeKey={value} onChange={onChange} className={className}>{items.map(item => <STabs.TabPane itemKey={item.key} tab={item.label} key={item.key}>{item.key === value && item.children}</STabs.TabPane>)}</STabs>; }
