import {
  ClockCircleOutlined,
  DeleteOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Badge,
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Popconfirm,
  Row,
  Skeleton,
  Space,
  Switch,
  Tag,
  Typography,
  notification,
} from "antd";
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
  getErrorMessage,
} from "../components/common/CommonUI";
import { invalidateWorkbenchQueries } from "../lib/useDashboardEvents";

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
      notification.success({
        message: updated.enabled ? "定时计划已启用" : "定时计划已暂停",
        description: `计划 “${updated.name}” 状态更新成功`,
      });
    },
    onError: (err) => {
      notification.error({
        message: "更新失败",
        description: getErrorMessage(err),
      });
    },
  });

  const runSchedule = useMutation({
    mutationFn: (id: number) => runArchiveSchedule(id),
    onSuccess: (jobs) => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "计划运行已触发",
        description: `已生成并排入 ${jobs.length} 个下载任务`,
      });
    },
    onError: (err) => {
      notification.error({
        message: "执行失败",
        description: getErrorMessage(err),
      });
    },
  });

  const removeSchedule = useMutation({
    mutationFn: (id: number) => deleteArchiveSchedule(id),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "计划已删除",
      });
    },
    onError: (err) => {
      notification.error({
        message: "删除失败",
        description: getErrorMessage(err),
      });
    },
  });

  return (
    <div className="space-y-5">
      {/* 顶部标题与行动 */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-3 border-b border-slate-200 dark:border-slate-800/80">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100 tracking-tight">
            自动归档计划
          </h1>
          <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
            配置定时轮询任务，定时扫描指定用户时间线、列表或关注成员，自动同步最新媒体
          </p>
        </div>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={onOpenCreateModal}
          className="!h-9 !rounded-xl !text-[13px] shadow-sm shadow-sky-500/20"
        >
          新建归档计划
        </Button>
      </div>

      {loading && schedules.length === 0 ? (
        <Row gutter={[16, 16]}>
          {Array.from({ length: 3 }, (_, index) => (
            <Col xs={24} md={12} lg={8} key={index}>
              <Card className="!rounded-2xl !border-slate-200 dark:!border-slate-800">
                <Skeleton active paragraph={{ rows: 5 }} />
              </Card>
            </Col>
          ))}
        </Row>
      ) : schedules.length === 0 ? (
        <Card className="!rounded-2xl !border-slate-200 dark:!border-slate-800 p-8 text-center">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={
              <div className="space-y-1">
                <Typography.Text strong className="block text-sm">
                  暂无定时归档计划
                </Typography.Text>
                <Typography.Text type="secondary" className="block text-xs max-w-md mx-auto">
                  您可以把常用关注的 X
                  博主、列表或推文账号加入自动计划，系统将按设置的频率自动同步最新媒体，免去手动重复输入的繁琐。
                </Typography.Text>
              </div>
            }
          >
            <Button type="primary" onClick={onOpenCreateModal} className="!rounded-xl !mt-2">
              立即创建第一个计划
            </Button>
          </Empty>
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {schedules.map((schedule) => (
            <Col xs={24} md={12} lg={8} key={schedule.id}>
              <Card
                className="!rounded-2xl !border-slate-200 dark:!border-slate-800 shadow-xs hover:border-sky-500/40 transition"
                title={
                  <Space size={6} className="max-w-[70%]">
                    <ClockCircleOutlined className="text-sky-500" />
                    <Typography.Text strong ellipsis className="text-sm">
                      {schedule.name}
                    </Typography.Text>
                  </Space>
                }
                extra={
                  <Switch
                    checked={schedule.enabled}
                    loading={
                      toggleEnabled.isPending &&
                      toggleEnabled.variables?.id === schedule.id
                    }
                    onChange={() => toggleEnabled.mutate(schedule)}
                  />
                }
                actions={[
                  <Popconfirm
                    key="delete"
                    title="删除计划"
                    description="确定要删除这个自动归档计划吗？已下载的文件不会被删除。"
                    okText="删除"
                    cancelText="取消"
                    onConfirm={() => removeSchedule.mutate(schedule.id)}
                  >
                    <Button
                      type="text"
                      danger
                      size="small"
                      icon={<DeleteOutlined />}
                      loading={
                        removeSchedule.isPending &&
                        removeSchedule.variables === schedule.id
                      }
                      className="!text-xs"
                    >
                      删除
                    </Button>
                  </Popconfirm>,
                  <Button
                    key="run"
                    type="link"
                    size="small"
                    icon={<PlayCircleOutlined />}
                    loading={
                      runSchedule.isPending &&
                      runSchedule.variables === schedule.id
                    }
                    onClick={() => runSchedule.mutate(schedule.id)}
                    className="!text-xs"
                  >
                    立即运行
                  </Button>,
                ]}
              >
                <div className="space-y-3">
                  {/* 目标列表 Tags */}
                  <div>
                    <Typography.Text type="secondary" className="!text-[11px] block mb-1">
                      归档目标 ({schedule.items.length} 个):
                    </Typography.Text>
                    <div className="flex flex-wrap gap-1 max-h-16 overflow-y-auto">
                      {schedule.items.map((item, idx) => (
                        <Tag
                          key={idx}
                          icon={<UserOutlined />}
                          color="cyan"
                          className="!rounded-md !text-[11px] !m-0"
                        >
                          {item.title || item.input}
                        </Tag>
                      ))}
                    </div>
                  </div>

                  {/* 调度信息 Descriptions */}
                  <div className="bg-slate-50 dark:bg-slate-950/60 p-2.5 rounded-xl border border-slate-200 dark:border-slate-800">
                    <Descriptions
                      size="small"
                      column={1}
                      items={[
                        {
                          key: "freq",
                          label: "执行频率",
                          children: (
                            <Typography.Text strong className="text-sky-600 dark:text-sky-400">
                              {formatIntervalMinutes(schedule.intervalMinutes)}
                            </Typography.Text>
                          ),
                        },
                        {
                          key: "last",
                          label: "上次执行",
                          children: (
                            <span className="font-mono text-xs text-slate-500">
                              {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "尚未执行"}
                            </span>
                          ),
                        },
                        {
                          key: "next",
                          label: "下次触发",
                          children: (
                            <Typography.Text strong type="success" className="font-mono text-xs">
                              {formatDateTime(schedule.nextRunAt)}
                            </Typography.Text>
                          ),
                        },
                      ]}
                    />
                  </div>
                </div>
              </Card>
            </Col>
          ))}
        </Row>
      )}
    </div>
  );
}
