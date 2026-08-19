import {
  RetweetOutlined,
  StopOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Progress,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  notification,
} from "antd";
import type { TableColumnsType } from "antd";
import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  cancelJob,
  retryJob,
  type Dashboard,
  type DownloadRecord,
  type FailedMedia,
  type Job,
} from "../../lib/api";
import {
  cancelableStatuses,
  progressStatus,
  retryableStatuses,
} from "../../lib/jobStatus";
import {
  AppEmpty,
  AppPagination,
  EllipsisText,
  Stack,
  clampPercent,
  formatDateTime,
  getErrorMessage,
  kindLabel,
  tablePageSizeOptions,
} from "../common/CommonUI";
import { JobDetails, groupDownloadsByJob, groupFailedMediaByJob } from "./JobDetails";
import { JobStatusTag } from "./JobStatusTag";

const { Text } = Typography;

export function JobTable({
  jobs,
  downloads,
  failed,
  pagination,
  onPageChange,
  onPageSizeChange,
}: {
  jobs: Job[];
  downloads: DownloadRecord[];
  failed: FailedMedia[];
  pagination: Dashboard["pagination"];
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const queryClient = useQueryClient();
  const [expandedJobIds, setExpandedJobIds] = useState<React.Key[]>([]);
  const manuallyCollapsedIds = useRef<Set<React.Key>>(new Set());
  const downloadsByJob = useMemo(() => groupDownloadsByJob(downloads), [downloads]);
  const failedByJob = useMemo(() => groupFailedMediaByJob(failed), [failed]);
  const activeJobIds = useMemo(
    () => jobs.filter((job) => cancelableStatuses.includes(job.status)).map((job) => job.id),
    [jobs],
  );

  useEffect(() => {
    if (activeJobIds.length === 0) return;

    setExpandedJobIds((current) => {
      const next = new Set(current);
      for (const id of activeJobIds) {
        if (!manuallyCollapsedIds.current.has(id)) {
          next.add(id);
        }
      }
      return next.size === current.length ? current : [...next];
    });
  }, [activeJobIds]);

  useEffect(() => {
    if (manuallyCollapsedIds.current.size === 0) return;
    const active = new Set<React.Key>(activeJobIds);
    for (const id of manuallyCollapsedIds.current) {
      if (!active.has(id)) {
        manuallyCollapsedIds.current.delete(id);
      }
    }
  }, [activeJobIds]);

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: `已创建重试任务 #${job.id}` });
    },
    onError: (error) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(error),
      });
    },
  });
  const cancel = useMutation({
    mutationFn: cancelJob,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "任务已取消" });
    },
    onError: (error) => {
      notification.error({
        message: "取消失败",
        description: getErrorMessage(error),
      });
    },
  });

  const columns: TableColumnsType<Job> = [
    {
      title: "任务",
      dataIndex: "title",
      key: "title",
      minWidth: 280,
      render: (_, job) => (
        <Stack size={2}>
          <Space size={8} wrap>
            <Text strong>{job.title || kindLabel(job.kind)}</Text>
            <Tag>{kindLabel(job.kind)}</Tag>
          </Space>
          <EllipsisText type="secondary" title={job.input}>
            {job.input}
          </EllipsisText>
        </Stack>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      key: "status",
      width: 116,
      render: (status: Job["status"]) => <JobStatusTag status={status} />,
    },
    {
      title: "进度",
      dataIndex: "progress",
      key: "progress",
      minWidth: 220,
      render: (_, job) => (
        <Stack size={4}>
          <Progress percent={clampPercent(job.progress)} size="small" status={progressStatus(job)} />
          <EllipsisText type={job.error ? "danger" : "secondary"} title={job.error || job.message}>
            {job.error || job.message || "暂无消息"}
          </EllipsisText>
        </Stack>
      ),
    },
    {
      title: "更新",
      dataIndex: "updatedAt",
      key: "updatedAt",
      width: 132,
      render: (value: string) => <Text type="secondary">{formatDateTime(value)}</Text>,
    },
    {
      title: "操作",
      key: "actions",
      width: 112,
      fixed: "right",
      render: (_, job) => {
        const canCancel = cancelableStatuses.includes(job.status);
        const canRetry = retryableStatuses.includes(job.status);
        const isCanceling = cancel.isPending && cancel.variables === job.id;
        const isRetrying = retry.isPending && retry.variables === job.id;
        return (
          <Space size={4}>
            <Tooltip title={canCancel ? "取消" : "当前状态不能取消"}>
              <Button
                size="small"
                icon={<StopOutlined />}
                disabled={!canCancel}
                loading={isCanceling}
                onClick={() => cancel.mutate(job.id)}
              />
            </Tooltip>
            <Tooltip title={canRetry ? "新建重试任务" : "运行中任务不能重试"}>
              <Button
                size="small"
                icon={<RetweetOutlined />}
                disabled={!canRetry}
                loading={isRetrying}
                onClick={() => retry.mutate(job.id)}
              />
            </Tooltip>
          </Space>
        );
      },
    },
  ];

  return (
    <Stack size={8}>
      <Table<Job>
        rowKey="id"
        columns={columns}
        dataSource={jobs}
        pagination={false}
        scroll={{ x: 860 }}
        expandable={{
          expandedRowKeys: expandedJobIds,
          onExpandedRowsChange: (keys) => {
            const next = new Set<React.Key>(keys);
            for (const id of expandedJobIds) {
              if (!next.has(id)) {
                manuallyCollapsedIds.current.add(id);
              }
            }
            setExpandedJobIds([...keys]);
          },
          expandedRowRender: (job) => (
            <JobDetails
              job={job}
              downloads={downloadsByJob.get(job.id) ?? []}
              failed={failedByJob.get(job.id) ?? []}
            />
          ),
        }}
        locale={{ emptyText: <AppEmpty description="暂无任务" /> }}
      />
      <AppPagination
        current={pagination.total > 0 ? pagination.page : 1}
        itemName="个任务"
        pageSize={pagination.pageSize}
        pageSizeOptions={tablePageSizeOptions}
        total={pagination.total}
        onChange={(page, pageSize) => {
          if (pageSize !== pagination.pageSize) {
            onPageSizeChange(pageSize);
            return;
          }
          onPageChange(page);
        }}
      />
    </Stack>
  );
}
