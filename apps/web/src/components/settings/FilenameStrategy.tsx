import React, { useEffect, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { t, useTranslation } from "../../lib/i18n";
import { Select } from "../semi/controls";
import { Tooltip } from "../semi/overlays";

const presetValues = new Set([
  "{tweet_id}_{media_id}_{index}.{ext}",
  "{username}_{tweet_id}_{index}.{ext}",
  "{text}_{tweet_id}_{index}.{ext}",
  "{media_id}.{ext}",
]);

export function FilenameStrategy({ value, onChange, disabled, labelPrefix }: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  labelPrefix: string;
}) {
  useTranslation();
  const presets = [
    { value: "{tweet_id}_{media_id}_{index}.{ext}", label: t("推文编号 + 媒体编号") },
    { value: "{username}_{tweet_id}_{index}.{ext}", label: t("用户名 + 推文编号") },
    { value: "{text}_{tweet_id}_{index}.{ext}", label: t("推文正文 + 推文编号") },
    { value: "{media_id}.{ext}", label: t("媒体编号") },
  ];
  const fields = [
    { value: "{text}", label: t("正文") },
    { value: "{tweet_id}", label: t("推文编号") },
    { value: "{media_id}", label: t("媒体编号") },
    { value: "{username}", label: t("用户名") },
    { value: "{index}", label: t("媒体序号") },
  ];
  const composable = /^\{(?:text|tweet_id|media_id|username|index)\}(?:_\{(?:text|tweet_id|media_id|username|index)\})*\.\{ext\}$/.test(value);
  const [editing, setEditing] = useState(false);
  const internalValueRef = useRef<string | null>(null);
  useEffect(() => {
    if (internalValueRef.current === value) {
      internalValueRef.current = null;
      return;
    }
    internalValueRef.current = null;
    if (presetValues.has(value)) {
      setEditing(false);
    }
  }, [value]);
  const custom = composable && (editing || !presets.some(item => item.value === value));
  const parts: string[] = composable ? value.slice(0, -".{ext}".length).match(/\{[^}]+\}/g) ?? [] : [];
  const options = [...presets, { value: "custom", label: t("自定义组合") }];
  if (!composable && !presets.some(item => item.value === value)) options.push({ value, label: t("保留现有命名方式") });
  const aria = (label: string) => [labelPrefix, label].filter(Boolean).join(" ");
  function changeParts(next: string[]) {
    const nextValue = `${next.join("_")}.{ext}`;
    if (nextValue === value) return;
    internalValueRef.current = nextValue;
    onChange(nextValue);
  }

  return <div className="storage-filename-strategy">
    <Select disabled={disabled} ariaLabel={aria(t("命名策略"))} value={custom ? "custom" : value} options={options} onChange={next => {
      const nextValue = next === "custom" ? composable ? value : "{text}.{ext}" : next;
      setEditing(next === "custom");
      if (nextValue === value) return;
      internalValueRef.current = nextValue;
      onChange(nextValue);
    }} />
    {custom && <div className="storage-filename-parts">
      {parts.map((part, index) => {
        const label = fields.find(item => item.value === part)!.label;
        return <div className="storage-filename-part" key={`${part}-${index}`}>
          <span>{label}</span>
          {([-1, 1] as const).map(direction => {
            const action = t(direction < 0 ? "上移" : "下移");
            return <Tooltip content={action} key={direction}><Button size="small" type="tertiary" theme="borderless" icon={direction < 0 ? <ArrowUp size={14} /> : <ArrowDown size={14} />} aria-label={aria(`${label} ${action}`)} disabled={disabled || index + direction < 0 || index + direction >= parts.length} onClick={() => {
              const next = [...parts];
              [next[index], next[index + direction]] = [next[index + direction], next[index]];
              changeParts(next);
            }} /></Tooltip>;
          })}
          <Tooltip content={t("移除")}><Button size="small" type="tertiary" theme="borderless" icon={<X size={14} />} aria-label={aria(`${label} ${t("移除")}`)} disabled={disabled || parts.length === 1} onClick={() => changeParts(parts.filter((_, position) => position !== index))} /></Tooltip>
        </div>;
      })}
      {fields.some(field => !parts.includes(field.value)) && <Select motion={false} disabled={disabled} ariaLabel={aria(t("添加命名字段"))} placeholder={t("添加命名字段")} value="" options={fields.filter(field => !parts.includes(field.value))} onChange={part => changeParts([...parts, part])} />}
    </div>}
  </div>;
}
