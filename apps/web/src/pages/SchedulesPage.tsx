import {
  ClockCircleOutlined,
  DeleteOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Flex,
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
  notifyError,
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
    onError: notifyError("更新失败"),
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
    onError: notifyError("执行失败"),
  });

  const removeSchedule = useMutation({
    mutationFn: (id: number) => deleteArchiveSchedule(id),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "计划已删除",
      });
    },
    onError: notifyError("删除失败"),
  });

  return (
    <div className="page-stack">
      {/* 顶部标题与行动 */}
      <Flex
        className="page-header"
        align="center"
        justify="space-between"
        gap={16}
        wrap="wrap"
      >
        <div>
          <Typography.Title level={4} style={{ margin: 0 }}>
            自动归档计划
          </Typography.Title>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            配置定时轮询任务，定时扫描指定用户时间线、列表或关注成员，自动同步最新媒体
          </Typography.Text>
        </div>
        <Button type="primary" icon={<PlusOutlined />} onClick={onOpenCreateModal}>
          新建归档计划
        </Button>
      </Flex>

      {loading && schedules.length === 0 ? (
        <Row gutter={[16, 16]}>
          {Array.from({ length: 3 }, (_, index) => (
            <Col xs={24} md={12} lg={8} key={index}>
              <Card>
                <Skeleton active paragraph={{ rows: 5 }} />
              </Card>
            </Col>
          ))}
        </Row>
      ) : schedules.length === 0 ? (
        <Card style={{ textAlign: "center", padding: 24 }}>
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
            <Button type="primary" onClick={onOpenCreateModal}>
              立即创建第一个计划
            </Button>
          </Empty>
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {schedules.map((schedule) => (
            <Col xs={24} md={12} lg={8} key={schedule.id}>
              <Card
                hoverable
                title={
                  <Space size={6} className="max-w-[70%]">
                    <ClockCircleOutlined style={{ color: "var(--brand-500)" }} />
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
                    
                  >
                    立即运行
                  </Button>,
                ]}
              >
                <div className="space-y-3">
                  {/* 目标列表 Tags */}
                  <div>
                    <Typography.Text type="secondary" className="block mb-1" style={{ fontSize: 11 }}>
                      归档目标 ({schedule.items.length} 个):
                    </Typography.Text>
                    <div className="flex flex-wrap gap-1 max-h-16 overflow-y-auto">
                      {schedule.items.map((item, idx) => (
                        <Tag
                          key={idx}
                          icon={<UserOutlined />}
                          style={{ margin: 0, fontSize: 11 }}
                        >
                          {item.title || item.input}
                        </Tag>
                      ))}
                    </div>
                  </div>

                  {/* 调度信息 Descriptions */}
                  <div className="p-2.5 rounded-lg" style={{ background: "var(--app-surface-muted)", border: "1px solid var(--app-border)" }}>
                    <Descriptions
                      size="small"
                      column={1}
                      items={[
                        {
                          key: "freq",
                          label: "执行频率",
                          children: (
                            <Typography.Text strong style={{ color: "var(--brand-600)" }}>
                              {formatIntervalMinutes(schedule.intervalMinutes)}
                            </Typography.Text>
                          ),
                        },
                        {
                          key: "last",
                          label: "上次执行",
                          children: (
                            <span className="font-mono" style={{ fontSize: 12, color: "var(--text-muted)" }}>
                              {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "尚未执行"}
                            </span>
                          ),
                        },
                        {
                          key: "next",
                          label: "下次触发",
                          children: (
                            <Typography.Text strong type="success" className="font-mono" style={{ fontSize: 12 }}>
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
