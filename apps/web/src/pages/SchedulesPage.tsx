import { t, useTranslation } from "../lib/i18n";
import {
  Clock,
  Play,
  Plus,
  Trash2,
  Users,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React from "react";
import { Button, Card, Switch } from "@douyinfe/semi-ui";
import {
  deleteArchiveSchedule,
  runArchiveSchedule,
  updateArchiveSchedule,
  type ArchiveSchedule,
} from "../lib/api";
import {
  formatDateTime,
  formatIntervalMinutes,
  notifyError,
} from "../components/common/CommonUI";
import { invalidateWorkbenchQueries } from "../lib/useDashboardEvents";
import { Alert, Empty, Skeleton } from "../components/semi";
import { Popconfirm } from "../components/semi";
import { Tag } from "../components/semi";
import { toast } from "../components/semi";

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

  const toggleEnabled = useMutation({
    mutationFn: (schedule: ArchiveSchedule) =>
      updateArchiveSchedule(schedule.id, {
        name: schedule.name,
        intervalMinutes: schedule.intervalMinutes,
        items: schedule.items,
        enabled: !schedule.enabled,
      }),
    onSuccess: (updated) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: updated.enabled ? t("定时计划已启用") : t("定时计划已暂停"),
        description: t("计划 “{{value0}}” 状态更新成功", { value0: updated.name }),
      });
    },
    onError: notifyError(t("更新失败")),
  });

  const runSchedule = useMutation({
    mutationFn: (id: number) => runArchiveSchedule(id),
    onSuccess: (jobs) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: t("计划运行已触发"),
        description: t("已生成并排入 {{value0}} 个下载任务", { value0: jobs.length }),
      });
    },
    onError: notifyError(t("执行失败")),
  });

  const removeSchedule = useMutation({
    mutationFn: (id: number) => deleteArchiveSchedule(id),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: t("计划已删除"),
      });
    },
    onError: notifyError(t("删除失败")),
  });

  return (
    <div className="workspace-page">
      <div className="page-heading">
        <h1 className="page-title">{t("自动归档计划")}</h1>
        <Button
          theme="solid"
          icon={<Plus className="size-4" />}
          onClick={onOpenCreateModal}
        >
          {t("新建归档计划")}</Button>
      </div>

      {error ? (
        <Alert type="error" showIcon message={t("定时计划加载失败")} description={error} />
      ) : loading && schedules.length === 0 ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {Array.from({ length: 3 }, (_, index) => (
            <Card key={index} bodyStyle={{ padding: 16 }}>
              <div className="space-y-3 p-1">
                <Skeleton className="h-5 w-3/5" />
                <Skeleton className="h-4 w-full" />
                <Skeleton className="h-4 w-4/5" />
                <Skeleton className="h-8 w-full mt-4" />
              </div>
            </Card>
          ))}
        </div>
      ) : schedules.length === 0 ? (
          <Empty
            description={
              <div className="space-y-1.5">
                <div className="font-semibold text-sm text-fg">{t("暂无定时归档计划")}</div>
                <p className="text-xs text-fg-muted max-w-md mx-auto">
                  {t("您可以把常用关注的 X 博主、列表或推文账号加入自动计划，系统将按设置的频率自动同步最新媒体，免去手动重复输入的繁琐。")}</p>
              </div>
            }
          >
            <Button
              theme="solid"
              icon={<Plus className="size-3.5" />}
              onClick={onOpenCreateModal}
              className="mt-2"
            >
              {t("立即创建第一个计划")}</Button>
          </Empty>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {schedules.map((schedule) => (
            <Card
              key={schedule.id}
              shadows="hover"
              title={
                <div className="flex min-w-0 items-center gap-2">
                  <Clock className="size-4 shrink-0 text-brand-500" />
                  <span className="truncate font-semibold text-sm text-fg" title={schedule.name}>
                    {schedule.name}
                  </span>
                </div>
              }
              headerExtraContent={
                <div className="flex items-center gap-2">
                  <span className="text-xs text-fg-muted font-normal">
                    {schedule.enabled ? t("运行中") : t("已暂停")}
                  </span>
                  <Switch
                    checked={schedule.enabled}
                    loading={toggleEnabled.isPending && toggleEnabled.variables?.id === schedule.id}
                    onChange={() => toggleEnabled.mutate(schedule)}
                    disabled={toggleEnabled.isPending && toggleEnabled.variables?.id === schedule.id}
                    aria-label={t("切换计划 {{value0}} 状态", { value0: schedule.name })}
                  />
                </div>
              }
              bodyStyle={{ padding: 16 }}
            >
              <div className="space-y-4">
              {/* 核心参数 */}
              <div className="grid grid-cols-2 gap-2 text-xs">
                <div className="min-w-0">
                  <span className="text-fg-subtle block text-xs">{t("执行周期")}</span>
                  <span className="font-medium text-fg mt-0.5 block">
                    {formatIntervalMinutes(schedule.intervalMinutes)}
                  </span>
                </div>
                <div className="min-w-0">
                  <span className="text-fg-subtle block text-xs">{t("下次运行")}</span>
                  <span className="font-mono text-fg mt-0.5 block truncate" title={schedule.nextRunAt}>
                    {schedule.nextRunAt ? formatDateTime(schedule.nextRunAt) : t("计算中")}
                  </span>
                </div>
              </div>

              {/* 目标列表标签 */}
              <div className="space-y-1">
                <div className="flex items-center justify-between text-xs text-fg-subtle">
                  <span>{t("目标清单")}</span>
                  <span>{schedule.items.length} {t(" 个目标")}</span>
                </div>
                <div className="flex flex-wrap gap-1 max-h-20 overflow-y-auto">
                  {schedule.items.map((item, idx) => (
                    <span
                      key={idx}
                      className="inline-flex items-center gap-1 rounded bg-surface-muted px-1.5 py-0.5 font-mono text-xs text-fg-body border border-line/60 truncate max-w-[180px]"
                      title={item.input}
                    >
                      <Users className="size-3 text-fg-subtle shrink-0" />
                      <span className="truncate">{item.input}</span>
                    </span>
                  ))}
                </div>
              </div>

              {/* 上次运行与操作栏 */}
              <div className="flex flex-wrap items-center justify-between gap-2 pt-3 border-t border-line text-xs">
                <span className="min-w-0 text-xs text-fg-subtle font-mono">
                  {t("上次: ")}{schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : t("从未")}
                </span>

                <div className="flex items-center gap-1.5">
                  <Popconfirm
                    title={t("删除定时计划")}
                    description={t("确定删除计划 “{{value0}}”？已下载的媒体文件不受影响。", { value0: schedule.name })}
                    okText={t("删除")}
                    cancelText={t("取消")}
                    onConfirm={() => removeSchedule.mutate(schedule.id)}
                  >
                    <Button
                      theme="borderless"
                      type="danger"
                      icon={<Trash2 className="size-3.5 text-danger" />}
                      loading={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                      disabled={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                      aria-busy={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                      aria-label={t("删除计划")}
                    />
                  </Popconfirm>

                  <Button
                    type="tertiary"
                    icon={<Play className="size-3.5" />}
                    loading={runSchedule.isPending && runSchedule.variables === schedule.id}
                    disabled={runSchedule.isPending && runSchedule.variables === schedule.id}
                    aria-busy={runSchedule.isPending && runSchedule.variables === schedule.id}
                    onClick={() => runSchedule.mutate(schedule.id)}
                  >
                    {t("立即执行")}</Button>
                </div>
              </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
