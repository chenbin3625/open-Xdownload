import { t, useTranslation } from "../lib/i18n";
import { Clock, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useMemo, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import {
  deleteArchiveSchedule,
  runArchiveSchedule,
  updateArchiveSchedule,
  type ArchiveSchedule,
} from "../lib/api";
import {
  formatDateTime,
  formatIntervalMinutes,
  kindLabel,
  notifyError,
} from "../components/common/CommonUI";
import { invalidateWorkbenchQueries } from "../lib/useDashboardEvents";
import { Alert, Empty, ListSkeleton } from "../components/semi/feedback";
import { Field } from "../components/semi/controls";
import { Drawer, Modal, Popconfirm, Tooltip } from "../components/semi/overlays";
import { Tag } from "../components/semi/data";
import { toast } from "../components/semi/toast";

type EditingSchedule = Omit<ArchiveSchedule, "intervalMinutes"> & {
  intervalMinutes: number | string;
};

function parseIntervalMinutes(value: number | string): number {
  if (typeof value === "string") {
    if (value.trim() === "") return Number.NaN;
    return Number(value);
  }
  return value;
}

function isValidIntervalMinutes(value: number): boolean {
  return Number.isInteger(value) && value >= 5 && value <= 43200;
}

function localTimeZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch { return "UTC"; }
}

function runStatusLabel(status: ArchiveSchedule["lastRunStatus"]) {
  if (status === "running") return { label: t("执行中"), tone: "brand" as const };
  if (status === "success") return { label: t("上轮成功"), tone: "success" as const };
  if (status === "failed") return { label: t("上轮失败"), tone: "danger" as const };
  if (status === "canceled") return { label: t("上轮已取消"), tone: "default" as const };
  return null;
}

export interface SchedulesPageProps {
  schedules: ArchiveSchedule[];
  loading?: boolean;
  error?: string;
  onOpenCreateModal: () => void;
}

export function SchedulesPage({
  schedules,
  loading = false,
  error,
  onOpenCreateModal,
}: SchedulesPageProps) {
  useTranslation();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [detailId, setDetailId] = useState<number | null>(null);
  const [editing, setEditing] = useState<EditingSchedule | null>(null);
  const [lastRun, setLastRun] = useState<number[] | null>(null);
  const timeZone = useMemo(() => localTimeZone(), []);
  const detail = schedules.find((schedule) => schedule.id === detailId);
  const detailRunStatus = runStatusLabel(detail?.lastRunStatus);
  const editingInterval = editing ? parseIntervalMinutes(editing.intervalMinutes) : Number.NaN;
  const editingIntervalValid = isValidIntervalMinutes(editingInterval);
  const filtered = schedules.filter(
    (schedule) =>
      (status === "all" || schedule.enabled === (status === "enabled")) &&
      `${schedule.name} ${schedule.items.map((item) => item.input).join(" ")}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );

  const toggle = useMutation({
    mutationFn: (schedule: ArchiveSchedule) =>
      updateArchiveSchedule(schedule.id, {
        name: schedule.name,
        intervalMinutes: schedule.intervalMinutes,
        items: schedule.items,
        enabled: !schedule.enabled,
      }),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
    },
    onError: notifyError(t("更新失败")),
  });
  const save = useMutation({
    mutationFn: (schedule: EditingSchedule) => {
      const intervalMinutes = parseIntervalMinutes(schedule.intervalMinutes);
      if (!isValidIntervalMinutes(intervalMinutes)) {
        throw new Error(t("调度频率需在 5 至 43200 分钟之间"));
      }
      const trimmedName = schedule.name.trim();
      const trimmedItems = schedule.items.map((item) => ({
        ...item,
        input: item.input.trim(),
      }));
      if (!trimmedName || trimmedItems.length === 0 || trimmedItems.some((item) => !item.input)) {
        throw new Error(t("请填写完整的计划名称和目标"));
      }
      return updateArchiveSchedule(schedule.id, {
        name: trimmedName,
        intervalMinutes,
        enabled: schedule.enabled,
        items: trimmedItems,
      });
    },
    onSuccess: () => {
      setEditing(null);
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("计划已更新") });
    },
    onError: notifyError(t("更新失败")),
  });
  const run = useMutation({
    mutationFn: runArchiveSchedule,
    onSuccess: (jobs) => {
      setLastRun(jobs.map((job) => job.id));
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("计划运行已触发") });
    },
    onError: notifyError(t("执行失败")),
  });
  const remove = useMutation({
    mutationFn: deleteArchiveSchedule,
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("计划已删除") });
    },
    onError: notifyError(t("删除失败")),
  });

  return (
    <div className="workspace-page schedules-page">
      <div className="page-heading">
        <h1 className="page-title">{t("自动归档计划")}</h1>
        <Button
          theme="solid"
          icon={<Plus size={16} />}
          onClick={onOpenCreateModal}
        >
          {t("新建归档计划")}
        </Button>
      </div>
      <div className="schedule-summary">
        <span>
          <Clock size={16} />
          {t("全部计划")} <strong>{schedules.length}</strong>
        </span>
        <span>
          {t("已启用")}{" "}
          <strong>
            {schedules.filter((schedule) => schedule.enabled).length}
          </strong>
        </span>
        <span>
          {t("已暂停")}{" "}
          <strong>
            {schedules.filter((schedule) => !schedule.enabled).length}
          </strong>
        </span>
        <span className="text-xs text-fg-muted">{t("时区")}: {timeZone}</span>
      </div>
      <div className="page-toolbar">
        <div className="toolbar-filters">
          <Input
            className="toolbar-search"
            prefix={<Search size={16} />}
            value={search}
            onChange={setSearch}
            placeholder={t("搜索计划名称或目标...")}
            aria-label={t("搜索计划")}
            showClear
          />
          <Select
            className="toolbar-select"
            aria-label={t("计划状态")}
            value={status}
            onChange={(value) => setStatus(String(value))}
            optionList={[
              { value: "all", label: t("全部状态") },
              { value: "enabled", label: t("已启用") },
              { value: "paused", label: t("已暂停") },
            ]}
          />
        </div>
        <span className="toolbar-summary" aria-live="polite">
          {t("匹配计划")} {filtered.length}
        </span>
      </div>
      {lastRun && (
        <Alert
          type="success"
          message={
            <span>
              {t("计划运行已触发")} · {lastRun.map((id) => `#${id}`).join(", ")}{" "}
              <a className="text-brand-600" href="/tasks">
                {t("查看任务")}
              </a>
            </span>
          }
        />
      )}
      {error ? (
        <Alert
          type="error"
          message={t("定时计划加载失败")}
          description={error}
        />
      ) : loading && !schedules.length ? (
        <ListSkeleton />
      ) : !filtered.length ? (
        <Empty
          description={
            schedules.length ? t("没有匹配的计划") : t("暂无定时归档计划")
          }
        >
          <Button
            type={schedules.length ? "tertiary" : "primary"}
            onClick={() => {
              if (schedules.length) {
                setSearch("");
                setStatus("all");
              } else onOpenCreateModal();
            }}
          >
            {schedules.length ? t("清除筛选") : t("新建归档计划")}
          </Button>
        </Empty>
      ) : (
        <div className="schedule-list">
          <div className="schedule-list-heading">
            <span>{t("计划 / 目标")}</span>
            <span>{t("执行频率")}</span>
            <span>{t("下次运行")}</span>
            <span>{t("计划状态")}</span>
            <span>{t("操作")}</span>
          </div>
          {filtered.map((schedule) => {
            const runStatus = runStatusLabel(schedule.lastRunStatus);
            return <article className="schedule-row" key={schedule.id}>
              <div className="schedule-identity">
                <button
                  className="task-title-link"
                  type="button"
                  onClick={() => setDetailId(schedule.id)}
                >
                  {schedule.name}
                </button>
                <div className="text-xs text-fg-muted">
                  {schedule.items.length} {t("个目标")} ·{" "}
                  {schedule.items
                    .slice(0, 2)
                    .map((item) => item.input)
                    .join(", ")}
                  {schedule.items.length > 2 &&
                    ` +${schedule.items.length - 2}`}
                </div>
                <span className="text-xs text-fg-muted">
                  {t("上次: ")}
                  {schedule.lastRunAt
                    ? formatDateTime(schedule.lastRunAt)
                    : t("从未")}
                </span>
                {runStatus && <Tag tone={runStatus.tone}>{runStatus.label}</Tag>}
                {schedule.lastRunStatus === "failed" && schedule.lastRunMessage && <span className="text-xs text-danger break-words">{schedule.lastRunMessage}</span>}
              </div>
              <span className="text-sm tabular-nums">
                {formatIntervalMinutes(schedule.intervalMinutes)}
              </span>
              <time
                className="text-sm text-fg-muted tabular-nums"
                dateTime={schedule.nextRunAt}
              >
                {schedule.enabled
                  ? schedule.nextRunAt
                    ? formatDateTime(schedule.nextRunAt)
                    : t("计算中")
                  : "—"}
              </time>
              <div className="flex items-center gap-2">
                <Switch
                  size="small"
                  checked={schedule.enabled}
                  disabled={toggle.isPending || save.isPending}
                  loading={
                    toggle.isPending && toggle.variables?.id === schedule.id
                  }
                  onChange={() => toggle.mutate(schedule)}
                  aria-label={t("切换计划 {{value0}} 状态", {
                    value0: schedule.name,
                  })}
                />
                <span className="text-xs text-fg-muted">
                  {schedule.enabled ? t("已启用") : t("已暂停")}
                </span>
              </div>
              <div className="schedule-actions">
                <Tooltip content={t("编辑计划")}>
                  <Button
                    theme="borderless"
                    type="tertiary"
                    icon={<Pencil size={16} />}
                    aria-label={`${t("编辑计划")} ${schedule.name}`}
                    disabled={toggle.isPending || save.isPending}
                    onClick={() =>
                      setEditing({
                        ...schedule,
                        items: schedule.items.map((item) => ({ ...item })),
                      })
                    }
                  />
                </Tooltip>
                <Button
                  theme="borderless"
                  type="tertiary"
                  title={t("立即执行")}
                  aria-label={`${t("立即执行")} ${schedule.name}`}
                  disabled={run.isPending}
                  aria-busy={run.isPending && run.variables === schedule.id}
                  loading={run.isPending && run.variables === schedule.id}
                  icon={<Play size={16} />}
                  onClick={() => run.mutate(schedule.id)}
                />
                <Popconfirm
                  title={t("删除定时计划")}
                  description={t(
                    "确定删除计划 “{{value0}}”？已下载的媒体文件不受影响。",
                    { value0: schedule.name },
                  )}
                  onConfirm={() => { if (!remove.isPending) remove.mutate(schedule.id); }}
                >
                  <Button
                    theme="borderless"
                    type="danger"
                    icon={<Trash2 size={16} />}
                    aria-label={t("删除计划")}
                    disabled={remove.isPending}
                    loading={
                      remove.isPending && remove.variables === schedule.id
                    }
                  />
                </Popconfirm>
              </div>
            </article>;
          })}
        </div>
      )}
      <Drawer
        open={Boolean(detail)}
        onClose={() => setDetailId(null)}
        title={detail?.name}
        width="36rem"
      >
        {detail && (
          <div className="space-y-5">
            <div className="flex flex-wrap gap-2">
              <Tag tone={detail.enabled ? "success" : "default"}>
                {detail.enabled ? t("已启用") : t("已暂停")}
              </Tag>
              <Tag>{formatIntervalMinutes(detail.intervalMinutes)}</Tag>
              {detailRunStatus && <Tag tone={detailRunStatus.tone}>{detailRunStatus.label}</Tag>}
            </div>
            <dl className="schedule-detail-meta">
              <dt>{t("下次运行")}</dt>
              <dd>{detail.enabled ? formatDateTime(detail.nextRunAt) : "—"}</dd>
              <dt>{t("上次运行")}</dt>
              <dd>
                {detail.lastRunAt
                  ? formatDateTime(detail.lastRunAt)
                  : t("从未")}
              </dd>
              <dt>{t("时区")}</dt>
              <dd>{timeZone}</dd>
              {detail.lastRunMessage && <><dt>{t("执行摘要")}</dt><dd className={detail.lastRunStatus === "failed" ? "text-danger" : undefined}>{detail.lastRunMessage}</dd></>}
            </dl>
            <h2 className="text-sm font-semibold text-fg">
              {t("目标清单")} ({detail.items.length})
            </h2>
            {detail.items.map((item, index) => (
              <div key={index} className="border-b border-line pb-3">
                <div className="text-sm font-semibold text-fg break-all">
                  {item.input}
                </div>
                <div className="flex flex-wrap gap-2 mt-2">
                  <Tag>{kindLabel(item.kind)}</Tag>
                  <Tag>
                    {item.archiveMode === "media"
                      ? t("archive.mediaOnly")
                      : item.archiveMode === "tweets"
                        ? t("archive.textOnly")
                        : t("archive.both")}
                  </Tag>
                  {item.backfill && <Tag>{t("archive.backfill")}</Tag>}
                </div>
              </div>
            ))}
            {detail.lastJobIds?.length > 0 && (
              <div>
                <h2 className="text-sm font-semibold text-fg mb-2">
                  {t("最近执行任务")}
                </h2>
                <a href="/tasks" className="text-sm text-brand-600">
                  {detail.lastJobIds.map((id) => `#${id}`).join(", ")} →
                </a>
              </div>
            )}
            <Button
              type="tertiary"
              icon={<Pencil size={16} />}
              onClick={() => {
                setEditing({
                  ...detail,
                  items: detail.items.map((item) => ({ ...item })),
                });
                setDetailId(null);
              }}
            >
              {t("编辑计划")}
            </Button>
          </div>
        )}
      </Drawer>
      <Modal
        open={Boolean(editing)}
        onClose={() => {
          if (!save.isPending) setEditing(null);
        }}
        title={t("编辑计划")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" disabled={save.isPending} onClick={() => setEditing(null)}>
              {t("取消")}
            </Button>
            <Button
              theme="solid"
              loading={save.isPending}
              disabled={
                !editing?.name.trim() ||
                !editing.items.length ||
                editing.items.some((item) => !item.input.trim()) ||
                save.isPending ||
                !editingIntervalValid
              }
              onClick={() => editing && save.mutate(editing)}
            >
              {t("保存计划")}
            </Button>
          </div>
        }
      >
        {editing && (
          <div className="space-y-4">
            <Field label={t("计划名称")}>
              <Input
                aria-label={t("计划名称")}
                value={editing.name}
                disabled={save.isPending}
                onChange={(name) => setEditing({ ...editing, name })}
              />
            </Field>
            <Field label={t("调度频率 (分钟)")}>
              <InputNumber
                min={5}
                precision={0}
                aria-label={t("调度频率 (分钟)")}
                max={43200}
                value={editing.intervalMinutes}
                disabled={save.isPending}
                onChange={(value) => {
                  setEditing({
                    ...editing,
                    intervalMinutes: value === "" || value === undefined || value === null ? "" : value,
                  });
                }}
              />
              <span className="text-xs text-fg-muted">
                {editingIntervalValid ? formatIntervalMinutes(editingInterval) : "—"}
              </span>
            </Field>
            <h3 className="text-sm font-semibold text-fg">{t("目标清单")}</h3>
            {editing.items.map((item, index) => (
              <div className="schedule-edit-target" key={index}>
                <Tag>{kindLabel(item.kind)}</Tag>
                <Input
                  aria-label={`${t("目标")} ${index + 1}`}
                  value={item.input}
                  disabled={save.isPending}
                  onChange={(input) =>
                    setEditing({
                      ...editing,
                      items: editing.items.map((current, position) =>
                        position === index ? { ...current, input, title: undefined } : current,
                      ),
                    })
                  }
                />
                <Tooltip content={t("移除目标")}>
                  <Button
                    theme="borderless"
                    type="danger"
                    icon={<Trash2 size={16} />}
                    aria-label={`${t("移除目标")} ${index + 1}`}
                    disabled={save.isPending}
                    onClick={() =>
                      setEditing({
                        ...editing,
                        items: editing.items.filter(
                          (_, position) => position !== index,
                        ),
                      })
                    }
                  />
                </Tooltip>
              </div>
            ))}
            <Button
              type="tertiary"
              icon={<Plus size={16} />}
              disabled={save.isPending}
              onClick={() =>
                setEditing({
                  ...editing,
                  items: [
                    ...editing.items,
                    { kind: "user", input: "", archiveMode: "both" },
                  ],
                })
              }
            >
              {t("添加用户目标")}
            </Button>
          </div>
        )}
      </Modal>
    </div>
  );
}
