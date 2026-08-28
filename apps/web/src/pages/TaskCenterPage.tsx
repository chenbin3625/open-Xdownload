import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CopyOutlined,
  ExclamationCircleOutlined,
  FileDoneOutlined,
  LoadingOutlined,
  PlusOutlined,
  ReloadOutlined,
  RetweetOutlined,
  SearchOutlined,
  StopOutlined,
  SyncOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Avatar,
  Badge,
  Button,
  Card,
  Descriptions,
  Empty,
  Input,
  Progress,
  Segmented,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
  notification,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import React, { useMemo, useState } from "react";
import {
  cancelJob,
  getJobFiles,
  jobFilesQueryRoot,
  retryFailedTweets,
  retryJob,
  type DashboardPagination,
  type DownloadRecord,
  type FailedMedia,
  type Job,
} from "../lib/api";
import {
  clampPercent,
  formatDateTime,
  getErrorMessage,
  kindLabel,
} from "../components/common/CommonUI";
import {
  cancelableStatuses,
  jobStatusBucket,
  progressStatus,
  retryableStatuses,
} from "../lib/jobStatus";
import { invalidateWorkbenchQueries } from "../lib/useDashboardEvents";

export type StatusFilterType = "all" | "active" | "completed" | "failed";

export interface TaskCenterPageProps {
  jobs: Job[];
  downloads?: DownloadRecord[];
  failed?: FailedMedia[];
  failedTweetCount: number;
  pagination: DashboardPagination;
  tableLoading?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  onOpenCreateModal: () => void;
  onOpenFailedDrawer: () => void;
}

export function TaskCenterPage({
  jobs,
  failedTweetCount,
  pagination,
  tableLoading = false,
  onPageChange,
  onPageSizeChange,
  onOpenCreateModal,
  onOpenFailedDrawer,
}: TaskCenterPageProps) {
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<StatusFilterType>("all");
  const [kindFilter, setKindFilter] = useState<string>("all");
  const [searchKeyword, setSearchKeyword] = useState<string>("");

  const cancel = useMutation({
    mutationFn: cancelJob,
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({ message: "任务已取消" });
    },
    onError: (err) => {
      notification.error({
        message: "取消失败",
        description: getErrorMessage(err),
      });
    },
  });

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({ message: `已创建重试任务 #${job.id}` });
    },
    onError: (err) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(err),
      });
    },
  });

  const retryAllFailed = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (newJob) => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({
        message: "已创建重试任务",
        description: newJob.title || "失败推文已重新加入执行队列",
      });
    },
    onError: (err) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(err),
      });
    },
  });

  // 本地根据当前页 items 进行实时过滤
  const filteredJobs = useMemo(() => {
    return jobs.filter((job) => {
      if (statusFilter === "active") {
        if (jobStatusBucket(job.status) !== "active") return false;
      } else if (statusFilter === "completed") {
        if (job.status !== "completed") return false;
      } else if (statusFilter === "failed") {
        if (
          job.status !== "failed" &&
          job.status !== "completed_with_errors"
        )
          return false;
      }

      if (kindFilter !== "all" && job.kind !== kindFilter) {
        return false;
      }

      if (searchKeyword.trim()) {
        const kw = searchKeyword.trim().toLowerCase();
        const matchesTitle = (job.title || "").toLowerCase().includes(kw);
        const matchesInput = (job.input || "").toLowerCase().includes(kw);
        const matchesId = String(job.id).includes(kw);
        if (!matchesTitle && !matchesInput && !matchesId) return false;
      }

      return true;
    });
  }, [jobs, statusFilter, kindFilter, searchKeyword]);

  const activeCount = useMemo(
    () => jobs.filter((j) => jobStatusBucket(j.status) === "active").length,
    [jobs],
  );
  const completedCount = useMemo(
    () => jobs.filter((j) => j.status === "completed").length,
    [jobs],
  );
  const failedCount = useMemo(
    () =>
      jobs.filter(
        (j) => j.status === "failed" || j.status === "completed_with_errors",
      ).length,
    [jobs],
  );

  const columns: ColumnsType<Job> = [
    {
      title: "任务信息 / 目标",
      dataIndex: "title",
      key: "title",
      width: 280,
      render: (_, record) => (
        <div className="flex items-center gap-3 py-1">
          <Avatar
            className="!bg-sky-500/15 !text-sky-600 dark:!text-sky-400 !font-bold shrink-0"
            size={32}
          >
            {record.kind === "user" ? "@" : "𝕏"}
          </Avatar>
          <div className="overflow-hidden max-w-xs md:max-w-md">
            <Typography.Text
              strong
              className="!text-[13px] text-slate-800 dark:text-slate-100 block truncate"
            >
              {record.title || kindLabel(record.kind)}
            </Typography.Text>
            <Typography.Text
              type="secondary"
              className="!text-[11px] !font-mono block truncate"
            >
              目标: {record.input} · #JOB-{record.id}
            </Typography.Text>
          </div>
        </div>
      ),
    },
    {
      title: "类型",
      dataIndex: "kind",
      key: "kind",
      width: 110,
      render: (kind) => (
        <Tag color="blue" className="!rounded-full !px-2 !text-[11px]">
          {kindLabel(kind)}
        </Tag>
      ),
    },
    {
      title: "当前状态",
      dataIndex: "status",
      key: "status",
      width: 130,
      render: (status) => {
        if (status === "resolving" || status === "downloading") {
          return (
            <Tag
              color="processing"
              icon={<SyncOutlined spin />}
              className="!rounded-full !px-2 !text-[11px]"
            >
              下载中
            </Tag>
          );
        }
        if (status === "completed") {
          return (
            <Tag
              color="success"
              icon={<CheckCircleOutlined />}
              className="!rounded-full !px-2 !text-[11px]"
            >
              已完成
            </Tag>
          );
        }
        if (status === "completed_with_errors") {
          return (
            <Tag
              color="warning"
              icon={<ExclamationCircleOutlined />}
              className="!rounded-full !px-2 !text-[11px]"
            >
              部分失败
            </Tag>
          );
        }
        if (status === "failed") {
          return (
            <Tag
              color="error"
              icon={<CloseCircleOutlined />}
              className="!rounded-full !px-2 !text-[11px]"
            >
              失败
            </Tag>
          );
        }
        return (
          <Tag color="default" className="!rounded-full !px-2 !text-[11px]">
            已取消
          </Tag>
        );
      },
    },
    {
      title: "执行进度",
      dataIndex: "progress",
      key: "progress",
      width: 220,
      render: (progress, record) => (
        <div className="space-y-1 py-1">
          <div className="flex justify-between text-[11px] font-mono text-slate-500 dark:text-slate-400">
            <span className="truncate max-w-[130px]">
              {record.message || "执行中..."}
            </span>
            <span className="font-semibold text-sky-500">
              {clampPercent(progress)}%
            </span>
          </div>
          <Progress
            percent={clampPercent(progress)}
            size="small"
            status={progressStatus(record)}
            strokeColor={{ "0%": "#0ea5e9", "100%": "#6366f1" }}
            showInfo={false}
          />
        </div>
      ),
    },
    {
      title: "更新时间",
      dataIndex: "updatedAt",
      key: "updatedAt",
      width: 160,
      render: (time) => (
        <span className="text-xs font-mono text-slate-500 dark:text-slate-400">
          {formatDateTime(time)}
        </span>
      ),
    },
    {
      title: "操作",
      key: "action",
      width: 140,
      align: "right",
      render: (_, record) => {
        const canCancel = cancelableStatuses.includes(record.status);
        const canRetry = retryableStatuses.includes(record.status);
        const isCanceling =
          cancel.isPending && cancel.variables === record.id;
        const isRetrying =
          retry.isPending && retry.variables === record.id;

        return (
          <Space size={6}>
            {canCancel && (
              <Button
                danger
                size="small"
                loading={isCanceling}
                onClick={() => cancel.mutate(record.id)}
                className="!rounded-lg !text-xs !h-7"
              >
                取消
              </Button>
            )}
            {canRetry && (
              <Button
                size="small"
                loading={isRetrying}
                onClick={() => retry.mutate(record.id)}
                className="!rounded-lg !text-xs !h-7"
              >
                重试
              </Button>
            )}
          </Space>
        );
      },
    },
  ];

  return (
    <div className="space-y-5">
      {/* 顶部页头与操作 (纯 Ant Design 组件) */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-3 border-b border-slate-200 dark:border-slate-800/80">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100 tracking-tight">
            任务调度中心
          </h1>
          <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
            监控所有已创建的推文与归档任务、过滤检索并查看下载文件记录
          </p>
        </div>
        <Space size={10} wrap>
          {failedTweetCount > 0 && (
            <Button
              danger
              icon={<CloseCircleOutlined />}
              onClick={onOpenFailedDrawer}
              className="!h-9 !rounded-xl !text-[13px]"
            >
              查看失败项 ({failedTweetCount})
            </Button>
          )}
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={onOpenCreateModal}
            className="!h-9 !rounded-xl !text-[13px] shadow-sm shadow-sky-500/20"
          >
            新建任务
          </Button>
        </Space>
      </div>

      {/* 过滤筛选工具栏 (纯 Ant Design 交互控件) */}
      <Card
        className="!rounded-2xl !border-slate-200 dark:!border-slate-800 shadow-xs"
        styles={{ body: { padding: "12px 16px" } }}
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-3 flex-wrap">
          {/* 状态分类 Segmented */}
          <Segmented
            value={statusFilter}
            onChange={(val) => setStatusFilter(val as StatusFilterType)}
            options={[
              { label: `全部 (${jobs.length})`, value: "all" },
              {
                label: (
                  <Space orientation="horizontal" size={4}>
                    {activeCount > 0 && <Badge status="processing" />}
                    <span>下载中 ({activeCount})</span>
                  </Space>
                ),
                value: "active",
              },
              { label: `已完成 (${completedCount})`, value: "completed" },
              {
                label: (
                  <Space orientation="horizontal" size={4}>
                    {failedCount > 0 && <Badge status="error" />}
                    <span>失败 / 异常 ({failedCount})</span>
                  </Space>
                ),
                value: "failed",
              },
            ]}
          />

          {/* 搜索、类型选择与重试操作 */}
          <Space size={10} wrap>
            <Input.Search
              placeholder="按标题、用户名或任务 ID 搜索..."
              value={searchKeyword}
              onChange={(e) => setSearchKeyword(e.target.value)}
              allowClear
              className="!w-64"
            />

            <Select
              value={kindFilter}
              onChange={setKindFilter}
              className="!w-36"
              options={[
                { value: "all", label: "全部任务类型" },
                { value: "tweet_link", label: "单条推文" },
                { value: "user", label: "用户媒体" },
                { value: "list", label: "列表媒体" },
                { value: "following", label: "关注媒体" },
                { value: "failed_retry", label: "重试任务" },
              ]}
            />

            {failedCount > 0 && (
              <Button
                icon={<RetweetOutlined className={retryAllFailed.isPending ? "animate-spin" : ""} />}
                loading={retryAllFailed.isPending}
                onClick={() => retryAllFailed.mutate()}
                className="!h-8 !rounded-lg !text-xs"
              >
                重试失败
              </Button>
            )}
          </Space>
        </div>
      </Card>

      {/* 任务核心数据表格 (纯 Ant Design Table) */}
      <Card
        className="!rounded-2xl !border-slate-200 dark:!border-slate-800 shadow-xs overflow-hidden"
        styles={{ body: { padding: 0 } }}
      >
        <Table<Job>
          columns={columns}
          dataSource={filteredJobs}
          rowKey="id"
          size="middle"
          scroll={{ x: 860 }}
          loading={tableLoading}
          pagination={{
            current: pagination.page,
            pageSize: pagination.pageSize,
            total: pagination.total,
            showSizeChanger: true,
            pageSizeOptions: ["10", "20", "50", "100"],
            onChange: (page, pageSize) => {
              if (pageSize !== pagination.pageSize) {
                onPageSizeChange(pageSize);
              } else {
                onPageChange(page);
              }
            },
            showTotal: (total) => `共 ${total} 个任务`,
          }}
          expandable={{
            expandedRowRender: (record) => (
              <ExpandedJobDetails
                job={record}
                onRetry={() => retry.mutate(record.id)}
              />
            ),
            rowExpandable: () => true,
          }}
        />
      </Card>
    </div>
  );
}

function ExpandedJobDetails({
  job,
  onRetry,
}: {
  job: Job;
  onRetry: () => void;
}) {
  const filesQuery = useQuery({
    queryKey: [...jobFilesQueryRoot, job.id],
    queryFn: ({ signal }) => getJobFiles(job.id, signal),
    staleTime: 60_000,
  });

  const downloads = filesQuery.data?.downloads ?? [];
  const failed = filesQuery.data?.failed ?? [];

  const copyPath = (text: string) => {
    void navigator.clipboard.writeText(text);
    notification.success({ message: "已复制到剪贴板", description: text });
  };

  return (
    <div className="p-4 bg-slate-50/70 dark:bg-slate-950/60 rounded-xl space-y-3 m-2 border border-slate-200/80 dark:border-slate-800/80">
      <div className="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400 flex-wrap gap-2">
        <Descriptions
          size="small"
          column={{ xs: 1, sm: 2, md: 3 }}
          items={[
            {
              key: "id",
              label: "任务 ID",
              children: <span className="font-mono">#{job.id}</span>,
            },
            {
              key: "created",
              label: "创建时间",
              children: <span className="font-mono">{formatDateTime(job.createdAt)}</span>,
            },
            {
              key: "mediaCount",
              label: "入库文件",
              children: (
                <Space>
                  <Tag color="success">成功 {downloads.length}</Tag>
                  {failed.length > 0 && <Tag color="error">失败 {failed.length}</Tag>}
                </Space>
              ),
            },
          ]}
        />
        {failed.length > 0 && (
          <Button size="small" type="link" onClick={onRetry}>
            重试此任务
          </Button>
        )}
      </div>

      {filesQuery.isLoading ? (
        <div className="py-6 text-center">
          <Spin indicator={<LoadingOutlined className="text-sky-500 text-lg" spin />} />
          <p className="text-xs text-slate-400 mt-2">正在获取已下载媒体文件清单...</p>
        </div>
      ) : downloads.length === 0 && failed.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无已归档的文件记录" />
      ) : (
        <div className="grid grid-cols-2 orientation-auto sm:grid-cols-4 md:grid-cols-6 gap-2 pt-2 border-t border-slate-200/60 dark:border-slate-800/60">
          {downloads.map((dl) => {
            const fileName = dl.filePath.split("/").pop() || dl.filePath;
            const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
            return (
              <Card
                key={dl.id}
                size="small"
                className="!bg-white dark:!bg-slate-900 !rounded-lg !border-slate-200 dark:!border-slate-800"
                styles={{ body: { padding: "8px 10px" } }}
              >
                <div className="flex items-center justify-between gap-1">
                  <div className="flex items-center gap-1.5 overflow-hidden">
                    <Tag color="cyan" className="!m-0 !px-1 !text-[10px] !font-mono">
                      {ext}
                    </Tag>
                    <Typography.Text
                      ellipsis
                      className="!text-[11px] !font-medium text-slate-700 dark:text-slate-300"
                    >
                      {fileName}
                    </Typography.Text>
                  </div>
                  <Tooltip title="复制文件本地完整路径">
                    <Button
                      type="text"
                      size="small"
                      icon={<CopyOutlined className="text-xs" />}
                      onClick={() => copyPath(dl.filePath)}
                      className="!h-6 !w-6 !p-0"
                    />
                  </Tooltip>
                </div>
              </Card>
            );
          })}

          {failed.map((fl) => (
            <Card
              key={fl.id}
              size="small"
              className="!bg-red-50/50 dark:!bg-red-950/30 !rounded-lg !border-red-200 dark:!border-red-800/50"
              styles={{ body: { padding: "8px 10px" } }}
            >
              <Typography.Text ellipsis type="danger" className="!text-[11px] !font-medium block">
                下载失败
              </Typography.Text>
              <Typography.Text ellipsis type="secondary" className="!text-[10px] block">
                {fl.error || fl.mediaUrl}
              </Typography.Text>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
