import {
  CloudDownloadOutlined,
  SyncOutlined,
  UnorderedListOutlined,
  UserAddOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Badge,
  Button,
  Col,
  Flex,
  Input,
  InputNumber,
  List,
  Row,
  Space,
  Tabs,
  Tag,
  Typography,
  notification,
} from "antd";
import type { TabsProps } from "antd";
import React, { useMemo, useState } from "react";
import {
  createArchiveSchedule,
  createJobsBatch,
  type JobKind,
  type JobRequest,
} from "../../lib/api";
import {
  EllipsisText,
  PaginatedList,
  Stack,
  Toolbar,
  formatIntervalMinutes,
  getErrorMessage,
  kindLabel,
} from "../common/CommonUI";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";

const { Text } = Typography;
const { TextArea } = Input;

export function BatchDownloadLauncher() {
  const queryClient = useQueryClient();
  const [users, setUsers] = useState("");
  const [lists, setLists] = useState("");
  const [following, setFollowing] = useState("");
  const [scheduleName, setScheduleName] = useState("");
  const [intervalMinutes, setIntervalMinutes] = useState(360);
  const items = useMemo(() => buildBatchDownloadItems(users, lists, following), [users, lists, following]);

  const createJobs = useMutation({
    mutationFn: () => createJobsBatch({ items }),
    onSuccess: (data) => {
      setUsers("");
      setLists("");
      setFollowing("");
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "批量任务已创建",
        description: `已创建 ${data.length} 个任务`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "创建失败",
        description: getErrorMessage(error),
      });
    },
  });

  const createSchedule = useMutation({
    mutationFn: () =>
      createArchiveSchedule({
        name: scheduleName.trim() || defaultArchiveScheduleName(items),
        enabled: true,
        intervalMinutes,
        items,
      }),
    onSuccess: (schedule) => {
      setScheduleName("");
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "定时计划已保存",
        description: `${schedule.name} · ${formatIntervalMinutes(schedule.intervalMinutes)}`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "保存失败",
        description: getErrorMessage(error),
      });
    },
  });

  const tabs: TabsProps["items"] = [
    {
      key: "users",
      label: (
        <Space>
          <UserOutlined />
          用户
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={users}
          onChange={setUsers}
          placeholder={"elonmusk\n1234567"}
        />
      ),
    },
    {
      key: "lists",
      label: (
        <Space>
          <UnorderedListOutlined />
          列表
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={lists}
          onChange={setLists}
          placeholder="8901234"
        />
      ),
    },
    {
      key: "following",
      label: (
        <Space>
          <UserAddOutlined />
          关注
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={following}
          onChange={setFollowing}
          placeholder={"567890\n@screen_name"}
        />
      ),
    },
  ];

  const previewSummary = items.length > 0 ? `准备创建 ${items.length} 个任务` : "输入目标后生成预览";

  return (
    <Stack size={14}>
      <Toolbar>
        <Space size={8} wrap>
          <Text type="secondary">待创建</Text>
          <Badge count={items.length} showZero color="blue" />
        </Space>
        <Space size={8} wrap>
          <Input
            value={scheduleName}
            onChange={(event) => setScheduleName(event.target.value)}
            placeholder="计划名称"
            style={{ width: 180 }}
          />
          <InputNumber
            min={5}
            max={43200}
            addonBefore="每"
            addonAfter="分钟"
            value={intervalMinutes}
            onChange={(value) => setIntervalMinutes(value ?? 5)}
            style={{ width: 170 }}
          />
          <Button
            icon={<SyncOutlined />}
            loading={createSchedule.isPending}
            disabled={items.length === 0}
            onClick={() => createSchedule.mutate()}
          >
            保存计划
          </Button>
          <Button
            type="primary"
            icon={<CloudDownloadOutlined />}
            loading={createJobs.isPending}
            disabled={items.length === 0}
            onClick={() => createJobs.mutate()}
          >
            批量下载
          </Button>
        </Space>
      </Toolbar>

      <Row gutter={[16, 16]} align="stretch">
        <Col xs={24} lg={14}>
          <Tabs items={tabs} />
        </Col>
        <Col xs={24} lg={10}>
          <Stack size={8}>
            <Flex align="center" justify="space-between" gap={10} wrap="wrap">
              <Text strong>任务预览</Text>
              <Text type="secondary">{previewSummary}</Text>
            </Flex>
            <PaginatedList
              bordered
              emptyDescription="暂无待创建任务"
              itemName="个任务"
              items={items}
              loading={createJobs.isPending || createSchedule.isPending}
              maxHeight={322}
              pageSize={6}
              renderItem={(item) => (
                <List.Item>
                  <List.Item.Meta
                    title={<Tag>{kindLabel(item.kind)}</Tag>}
                    description={
                      <EllipsisText title={item.input}>
                        {item.input}
                      </EllipsisText>
                    }
                  />
                </List.Item>
              )}
              size="small"
            />
          </Stack>
        </Col>
      </Row>
    </Stack>
  );
}

export function BatchTargetInput({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <TextArea
      value={value}
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      autoSize={{ minRows: 6, maxRows: 12 }}
    />
  );
}

export function buildBatchDownloadItems(users: string, lists: string, following: string): JobRequest[] {
  const items: JobRequest[] = [];
  for (const input of parseTargets(users)) {
    items.push({ kind: "user", input, title: `用户 ${displayDownloadTarget(input)}` });
  }
  for (const input of parseTargets(lists)) {
    items.push({ kind: "list", input, title: `列表 ${input}` });
  }
  for (const input of parseTargets(following)) {
    items.push({ kind: "following", input, title: `关注 ${displayDownloadTarget(input)}` });
  }
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = `${item.kind}:${item.input.toLowerCase()}`;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

export function defaultArchiveScheduleName(items: JobRequest[]) {
  if (items.length === 0) {
    return "批量归档计划";
  }
  const first = items[0];
  return `${kindLabel(first.kind)} ${first.input}${items.length > 1 ? ` 等 ${items.length} 个目标` : ""}`;
}

export function parseTargets(value: string) {
  return value
    .split(/[\n,，\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function displayDownloadTarget(input: string) {
  if (input.startsWith("@") || /^\d+$/.test(input)) {
    return input;
  }
  return `@${input}`;
}
