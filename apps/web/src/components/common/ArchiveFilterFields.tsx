import React, { useId } from "react";
import { t, useTranslation } from "../../lib/i18n";
import type { ArchiveFilter } from "../../lib/api";
import { Checkbox, Field, Input, NumberInput } from "../semi/controls";

export function isArchiveFilterEmpty(filter?: ArchiveFilter | null): boolean {
  if (!filter) return true;
  return (
    (!filter.mediaTypes || filter.mediaTypes.length === 0) &&
    !filter.excludeRetweets &&
    !filter.excludeReplies &&
    !(filter.minBytes && filter.minBytes > 0) &&
    !(filter.minWidth && filter.minWidth > 0) &&
    !(filter.minHeight && filter.minHeight > 0) &&
    !(filter.sinceDate && filter.sinceDate.trim()) &&
    !(filter.untilDate && filter.untilDate.trim())
  );
}

export function cleanArchiveFilter(filter?: ArchiveFilter | null): ArchiveFilter | undefined {
  if (!filter || isArchiveFilterEmpty(filter)) return undefined;
  const out: ArchiveFilter = {};
  if (filter.mediaTypes && filter.mediaTypes.length > 0) out.mediaTypes = filter.mediaTypes;
  if (filter.excludeRetweets) out.excludeRetweets = true;
  if (filter.excludeReplies) out.excludeReplies = true;
  if (filter.minBytes && filter.minBytes > 0) out.minBytes = Math.round(filter.minBytes);
  if (filter.minWidth && filter.minWidth > 0) out.minWidth = Math.round(filter.minWidth);
  if (filter.minHeight && filter.minHeight > 0) out.minHeight = Math.round(filter.minHeight);
  if (filter.sinceDate?.trim()) out.sinceDate = filter.sinceDate.trim();
  if (filter.untilDate?.trim()) out.untilDate = filter.untilDate.trim();
  return out;
}

export function countArchiveFilterRules(filter?: ArchiveFilter | null): number {
  if (!filter) return 0;
  let count = 0;
  if (filter.mediaTypes && filter.mediaTypes.length > 0) count += 1;
  if (filter.excludeRetweets) count += 1;
  if (filter.excludeReplies) count += 1;
  if (filter.minBytes && filter.minBytes > 0) count += 1;
  if ((filter.minWidth && filter.minWidth > 0) || (filter.minHeight && filter.minHeight > 0)) count += 1;
  if (filter.sinceDate?.trim() || filter.untilDate?.trim()) count += 1;
  return count;
}

export function ArchiveFilterFields({
  value,
  onChange,
}: {
  value?: ArchiveFilter | null;
  onChange: (next: ArchiveFilter | null) => void;
}) {
  useTranslation();
  const minBytesId = useId();
  const minWidthId = useId();
  const minHeightId = useId();
  const sinceId = useId();
  const untilId = useId();
  const current = value ?? {};
  const selectedTypes = current.mediaTypes ?? [];

  function patch(partial: Partial<ArchiveFilter>) {
    const merged: ArchiveFilter = { ...current, ...partial };
    onChange(cleanArchiveFilter(merged) ?? null);
  }

  function toggleMediaType(type: string, checked: boolean) {
    const next = checked
      ? Array.from(new Set([...selectedTypes, type]))
      : selectedTypes.filter((item) => item !== type);
    patch({ mediaTypes: next.length > 0 ? next : undefined });
  }

  const minMiB = current.minBytes && current.minBytes > 0
    ? Number((current.minBytes / 1048576).toFixed(2))
    : null;

  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-4">
        <span className="font-semibold text-fg-muted">{t("允许媒体类型")}</span>
        <Checkbox
          checked={selectedTypes.includes("photo")}
          onChange={(checked) => toggleMediaType("photo", checked)}
          label={t("图片")}
        />
        <Checkbox
          checked={selectedTypes.includes("video")}
          onChange={(checked) => toggleMediaType("video", checked)}
          label={t("视频")}
        />
        <Checkbox
          checked={selectedTypes.includes("animated_gif")}
          onChange={(checked) => toggleMediaType("animated_gif", checked)}
          label="GIF"
        />
        <span className="text-fg-subtle">{t("全不选表示不限制")}</span>
      </div>

      <div className="flex flex-wrap items-center gap-4">
        <Checkbox
          checked={Boolean(current.excludeRetweets)}
          onChange={(checked) => patch({ excludeRetweets: checked || undefined })}
          label={t("跳过转推")}
        />
        <Checkbox
          checked={Boolean(current.excludeReplies)}
          onChange={(checked) => patch({ excludeReplies: checked || undefined })}
          label={t("跳过回复")}
        />
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Field label={t("最小文件体积 (MiB)")} htmlFor={minBytesId}>
          <NumberInput
            id={minBytesId}
            min={0}
            max={10240}
            value={minMiB}
            placeholder={t("不限")}
            onChange={(val) => patch({ minBytes: val && val > 0 ? Math.round(val * 1048576) : undefined })}
            addonAfter="MiB"
          />
        </Field>
        <Field label={t("最小宽度 (px)")} htmlFor={minWidthId}>
          <NumberInput
            id={minWidthId}
            min={0}
            max={16384}
            value={current.minWidth || null}
            placeholder={t("不限")}
            onChange={(val) => patch({ minWidth: val && val > 0 ? Math.round(val) : undefined })}
            addonAfter="px"
          />
        </Field>
        <Field label={t("最小高度 (px)")} htmlFor={minHeightId}>
          <NumberInput
            id={minHeightId}
            min={0}
            max={16384}
            value={current.minHeight || null}
            placeholder={t("不限")}
            onChange={(val) => patch({ minHeight: val && val > 0 ? Math.round(val) : undefined })}
            addonAfter="px"
          />
        </Field>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label={t("发布开始日期")} htmlFor={sinceId}>
          <Input
            id={sinceId}
            type="date"
            value={current.sinceDate ?? ""}
            onChange={(e) => patch({ sinceDate: e.target.value || undefined })}
          />
        </Field>
        <Field label={t("发布结束日期")} htmlFor={untilId}>
          <Input
            id={untilId}
            type="date"
            value={current.untilDate ?? ""}
            onChange={(e) => patch({ untilDate: e.target.value || undefined })}
          />
        </Field>
      </div>
    </div>
  );
}
