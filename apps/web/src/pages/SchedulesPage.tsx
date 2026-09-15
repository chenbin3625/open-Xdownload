import {
  Clock,
  Play,
  Plus,
  Trash2,
  Users,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React from "react";
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
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { Switch } from "../components/ui/Controls";
import { Empty, Skeleton } from "../components/ui/Feedback";
import { Popconfirm } from "../components/ui/Overlay";
import { Tag } from "../components/ui/Tag";
import { toast } from "../components/ui/Toast";

export interface SchedulesPageProps {
  schedules: ArchiveSchedule[];
  loading?: boolean;
  onOpenCreateModal: () => void;
}

export function SchedulesPage({
  schedules,
  loading = false,
  onOpenCreateModal,
}: SchedulesPageProps) {
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
        message: updated.enabled ? "定时计划已启用" : "定时计划已暂停",
        description: `计划 “${updated.name}” 状态更新成功`,
      });
    },
    onError: notifyError("更新失败"),
  });

  const runSchedule = useMutation({
    mutationFn: (id: number) => runArchiveSchedule(id),
    onSuccess: (jobs) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: "计划运行已触发",
        description: `已生成并排入 ${jobs.length} 个下载任务`,
      });
    },
    onError: notifyError("执行失败"),
  });

  const removeSchedule = useMutation({
    mutationFn: (id: number) => deleteArchiveSchedule(id),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: "计划已删除",
      });
    },
    onError: notifyError("删除失败"),
  });

  return (
    <div className="flex flex-col gap-4">
      {/* 顶部标题与行动 */}
      <div className="flex flex-wrap items-center justify-between gap-4 pb-1">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">自动归档计划</h1>
          <p className="mt-0.5 text-xs text-fg-muted">
            配置定时轮询任务，定时扫描指定用户时间线、列表或关注成员，自动同步最新媒体
          </p>
        </div>
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          onClick={onOpenCreateModal}
        >
          新建归档计划
        </Button>
      </div>

      {loading && schedules.length === 0 ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {Array.from({ length: 3 }, (_, index) => (
            <Card key={index} size="sm">
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
        <Card className="text-center py-8">
          <Empty
            description={
              <div className="space-y-1.5">
                <div className="font-semibold text-sm text-fg">暂无定时归档计划</div>
                <p className="text-xs text-fg-muted max-w-md mx-auto">
                  您可以把常用关注的 X 博主、列表或推文账号加入自动计划，系统将按设置的频率自动同步最新媒体，免去手动重复输入的繁琐。
                </p>
              </div>
            }
          >
            <Button
              variant="primary"
              size="sm"
              icon={<Plus className="size-3.5" />}
              onClick={onOpenCreateModal}
              className="mt-2"
            >
              立即创建第一个计划
            </Button>
          </Empty>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {schedules.map((schedule) => (
            <Card
              key={schedule.id}
              hoverable
              size="sm"
              title={
                <div className="flex items-center gap-2 max-w-[80%]">
                  <Clock className="size-4 shrink-0 text-brand-500" />
                  <span className="truncate font-semibold text-sm text-fg" title={schedule.name}>
                    {schedule.name}
                  </span>
                </div>
              }
              extra={
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-fg-muted font-normal">
                    {schedule.enabled ? "运行中" : "已暂停"}
                  </span>
                  <Switch
                    checked={schedule.enabled}
                    loading={toggleEnabled.isPending && toggleEnabled.variables?.id === schedule.id}
                    onChange={() => toggleEnabled.mutate(schedule)}
                    ariaLabel={`切换计划 ${schedule.name} 状态`}
                  />
                </div>
              }
              bodyClassName="space-y-3"
            >
              {/* 核心参数 */}
              <div className="grid grid-cols-2 gap-2 text-xs">
                <div className="rounded-control bg-surface-muted/60 p-2 border border-line/60">
                  <span className="text-fg-subtle block text-[11px]">执行周期</span>
                  <span className="font-medium text-fg mt-0.5 block">
                    {formatIntervalMinutes(schedule.intervalMinutes)}
                  </span>
                </div>
                <div className="rounded-control bg-surface-muted/60 p-2 border border-line/60">
                  <span className="text-fg-subtle block text-[11px]">下次运行</span>
                  <span className="font-mono text-fg mt-0.5 block truncate" title={schedule.nextRunAt}>
                    {schedule.nextRunAt ? formatDateTime(schedule.nextRunAt) : "计算中"}
                  </span>
                </div>
              </div>

              {/* 目标列表标签 */}
              <div className="space-y-1">
                <div className="flex items-center justify-between text-[11px] text-fg-subtle">
                  <span>目标清单</span>
                  <span>{schedule.items.length} 个目标</span>
                </div>
                <div className="flex flex-wrap gap-1 max-h-20 overflow-y-auto">
                  {schedule.items.map((item, idx) => (
                    <span
                      key={idx}
                      className="inline-flex items-center gap-1 rounded bg-surface-muted px-1.5 py-0.5 font-mono text-[11px] text-fg-body border border-line/60 truncate max-w-[180px]"
                      title={item.input}
                    >
                      <Users className="size-3 text-fg-subtle shrink-0" />
                      <span className="truncate">{item.input}</span>
                    </span>
                  ))}
                </div>
              </div>

              {/* 上次运行与操作栏 */}
              <div className="flex items-center justify-between pt-2 border-t border-line text-xs">
                <span className="text-[11px] text-fg-subtle font-mono truncate max-w-[150px]">
                  上次: {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "从未"}
                </span>

                <div className="flex items-center gap-1.5">
                  <Popconfirm
                    title="删除定时计划"
                    description={`确定删除计划 “${schedule.name}”？已下载的媒体文件不受影响。`}
                    okText="删除"
                    cancelText="取消"
                    onConfirm={() => removeSchedule.mutate(schedule.id)}
                  >
                    <Button
                      variant="text"
                      size="sm"
                      icon={<Trash2 className="size-3.5 text-danger" />}
                      loading={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                      aria-label="删除计划"
                    />
                  </Popconfirm>

                  <Button
                    variant="default"
                    size="sm"
                    icon={<Play className="size-3" />}
                    loading={runSchedule.isPending && runSchedule.variables === schedule.id}
                    onClick={() => runSchedule.mutate(schedule.id)}
                  >
                    立即执行
                  </Button>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
