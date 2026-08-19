import {
  CloudDownloadOutlined,
  DeleteOutlined,
  SyncOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  List,
  Popconfirm,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
  notification,
} from "antd";
import React from "react";
import {
  deleteArchiveSchedule,
  runArchiveSchedule,
  updateArchiveSchedule,
  type ArchiveSchedule,
} from "../../lib/api";
import {
  EllipsisText,
  PaginatedList,
  Stack,
  formatDateTime,
  formatIntervalMinutes,
  getErrorMessage,
  iconStyles,
  kindLabel,
} from "../common/CommonUI";

const { Text } = Typography;

export function ArchiveScheduleList({ schedules }: { schedules: ArchiveSchedule[] }) {
  const queryClient = useQueryClient();
  const runSchedule = useMutation({
    mutationFn: runArchiveSchedule,
    onSuccess: (jobs) => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({
        message: "计划已开始运行",
        description: `已创建 ${jobs.length} 个任务`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "运行失败",
        description: getErrorMessage(error),
      });
    },
  });
  const toggleSchedule = useMutation({
    mutationFn: ({ schedule, enabled }: { schedule: ArchiveSchedule; enabled: boolean }) =>
      updateArchiveSchedule(schedule.id, {
        name: schedule.name,
        enabled,
        intervalMinutes: schedule.intervalMinutes,
        items: schedule.items,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "计划已更新" });
    },
    onError: (error) => {
      notification.error({
        message: "更新失败",
        description: getErrorMessage(error),
      });
    },
  });
  const removeSchedule = useMutation({
    mutationFn: deleteArchiveSchedule,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "计划已删除" });
    },
    onError: (error) => {
      notification.error({
        message: "删除失败",
        description: getErrorMessage(error),
      });
    },
  });

  return (
    <Stack size={8}>
      <PaginatedList
        bordered
        emptyDescription="暂无定时计划"
        itemName="个计划"
        items={schedules}
        pageSize={5}
        renderItem={(schedule) => (
          <List.Item
            actions={[
              <Switch
                key="enabled"
                size="small"
                checked={schedule.enabled}
                loading={toggleSchedule.isPending && toggleSchedule.variables?.schedule.id === schedule.id}
                onChange={(enabled) => toggleSchedule.mutate({ schedule, enabled })}
              />,
              <Tooltip key="run" title="立即运行">
                <Button
                  size="small"
                  type="text"
                  icon={<CloudDownloadOutlined />}
                  loading={runSchedule.isPending && runSchedule.variables === schedule.id}
                  onClick={() => runSchedule.mutate(schedule.id)}
                />
              </Tooltip>,
              <Popconfirm
                key="delete"
                title="删除定时计划"
                description="确认删除这个定时计划？"
                okText="删除"
                cancelText="取消"
                onConfirm={() => removeSchedule.mutate(schedule.id)}
              >
                <Button
                  size="small"
                  danger
                  type="text"
                  icon={<DeleteOutlined />}
                  loading={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                />
              </Popconfirm>,
            ]}
          >
            <List.Item.Meta
              avatar={<SyncOutlined spin={schedule.enabled} style={iconStyles.primary} />}
              title={
                <Space size={8} wrap>
                  <Text strong>{schedule.name}</Text>
                  <Tag color={schedule.enabled ? "processing" : "default"}>
                    {schedule.enabled ? "启用" : "停用"}
                  </Tag>
                  <Tag>{formatIntervalMinutes(schedule.intervalMinutes)}</Tag>
                </Space>
              }
              description={
                <Stack size={4}>
                  <Space size={10} wrap>
                    <Text type="secondary">目标 {schedule.items.length}</Text>
                    <Text type="secondary">下次 {formatDateTime(schedule.nextRunAt)}</Text>
                    <Text type="secondary">上次 {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "未运行"}</Text>
                  </Space>
                  <EllipsisText title={schedule.items.map((item) => `${kindLabel(item.kind)} ${item.input}`).join("，")}>
                    {schedule.items.map((item) => `${kindLabel(item.kind)} ${item.input}`).join("，") || "无目标"}
                  </EllipsisText>
                </Stack>
              }
            />
          </List.Item>
        )}
        size="small"
      />
    </Stack>
  );
}
