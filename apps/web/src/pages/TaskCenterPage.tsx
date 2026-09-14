import {
  CheckCircleOutlined,
  ClockCircleOutlined,
  CloseCircleOutlined,
  CopyOutlined,
  ExclamationCircleOutlined,
  PlusOutlined,
  RetweetOutlined,
  SyncOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  Card,
  Descriptions,
  Empty,
  Flex,
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
  copyToClipboard,
  formatDateTime,
  getErrorMessage,
  kindLabel,
  notifyError,
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

// 状态标签原先是一串 if / return，pending 会掉进兜底分支被渲染成“已取消”。
// 改为按状态查表，七种状态都有明确的文案与配色。
const jobStatusPresentation: Record<
  Job["status"],
  { label: string; color?: string; icon?: React.ReactNode }
> = {
  pending: { label: "排队中", color: "default", icon: <ClockCircleOutlined /> },
  resolving: { label: "下载中", color: "processing", icon: <SyncOutlined spin /> },
  downloading: { label: "下载中", color: "processing", icon: <SyncOutlined spin /> },
  completed: { label: "已完成", color: "success", icon: <CheckCircleOutlined /> },
  completed_with_errors: {
    label: "部分失败",
    color: "warning",
    icon: <ExclamationCircleOutlined />,
  },
  failed: { label: "失败", color: "error", icon: <CloseCircleOutlined /> },
  canceled: { label: "已取消" },
};

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
    onError: notifyError("取消失败"),
  });

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({ message: `已创建重试任务 #${job.id}` });
    },
    onError: notifyError("重试失败"),
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
    onError: notifyError("重试失败"),
  });

  // 本地根据当前页 items 进行实时过滤。
  // statusFilter 的取值与 jobStatusBucket 的分档一一对应，直接比对分档即可，
  // 不必在这里重复列举 completed / completed_with_errors 等具体状态。
  const filteredJobs = useMemo(() => {
    const keyword = searchKeyword.trim().toLowerCase();
    return jobs.filter((job) => {
      if (statusFilter !== "all" && jobStatusBucket(job.status) !== statusFilter) {
        return false;
      }
      if (kindFilter !== "all" && job.kind !== kindFilter) {
        return false;
      }
      if (!keyword) {
        return true;
      }
      return (
        (job.title || "").toLowerCase().includes(keyword) ||
        (job.input || "").toLowerCase().includes(keyword) ||
        String(job.id).includes(keyword)
      );
    });
  }, [jobs, statusFilter, kindFilter, searchKeyword]);

  // 过滤只作用于当前页的 items，服务端总数无法反映筛选结果：
  // 一旦启用筛选就改用本页命中数驱动分页，避免“显示 2 条 / 共 120 条”的自相矛盾。
  const hasActiveFilter =
    statusFilter !== "all" || kindFilter !== "all" || searchKeyword.trim() !== "";

  // 三档计数原先是三次独立 useMemo，各自全量遍历 jobs 并重复一遍状态判定，
  // 这里一次遍历按分档累加。
  const bucketCounts = useMemo(() => {
    const counts = { active: 0, completed: 0, failed: 0 };
    for (const job of jobs) {
      const bucket = jobStatusBucket(job.status);
      if (bucket !== "idle") counts[bucket] += 1;
    }
    return counts;
  }, [jobs]);

  const columns: ColumnsType<Job> = [
    {
      title: "任务信息 / 目标",
      dataIndex: "title",
      key: "title",
      width: 280,
      render: (_, record) => (
        <div className="flex items-center gap-3 py-1">
          <Avatar
            className="shrink-0" style={{ background: "var(--brand-100)", color: "var(--brand-600)", fontWeight: 700 }}
            size={32}
          >
            {record.kind === "user" ? "@" : "𝕏"}
          </Avatar>
          <div className="overflow-hidden max-w-xs md:max-w-md">
            <Typography.Text
              strong
              className="block truncate" style={{ fontSize: 13 }}
            >
              {record.title || kindLabel(record.kind)}
            </Typography.Text>
            <Typography.Text
              type="secondary"
              className="font-mono block truncate" style={{ fontSize: 11 }}
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
        <Tag color="processing">
          {kindLabel(kind)}
        </Tag>
      ),
    },
    {
      title: "当前状态",
      dataIndex: "status",
      key: "status",
      width: 130,
      render: (status: Job["status"]) => {
        const meta = jobStatusPresentation[status];
        return <Tag color={meta.color} icon={meta.icon}>{meta.label}</Tag>;
      },
    },
    {
      title: "执行进度",
      dataIndex: "progress",
      key: "progress",
      width: 220,
      render: (progress, record) => (
        <div className="space-y-1 py-1">
          <div className="flex justify-between font-mono" style={{ fontSize: 11, color: "var(--text-muted)" }}>
            <span className="truncate max-w-[130px]">
              {record.message || "执行中..."}
            </span>
            <span style={{ fontWeight: 600, color: "var(--brand-500)" }}>
              {clampPercent(progress)}%
            </span>
          </div>
          <Progress
            percent={clampPercent(progress)}
            size="small"
            status={progressStatus(record)}
            strokeColor="#0ea5e9"
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
        <span className="font-mono" style={{ fontSize: 12, color: "var(--text-muted)" }}>
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
              >
                取消
              </Button>
            )}
            {canRetry && (
              <Button
                size="small"
                loading={isRetrying}
                onClick={() => retry.mutate(record.id)}
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
    <div className="page-stack">
      {/* 顶部页头与操作 */}
      <Flex
        className="page-header"
        align="center"
        justify="space-between"
        gap={16}
        wrap="wrap"
      >
        <div>
          <Typography.Title level={4} style={{ margin: 0 }}>
            任务调度中心
          </Typography.Title>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            监控所有已创建的推文与归档任务、过滤检索并查看下载文件记录
          </Typography.Text>
        </div>
        <Space wrap>
          {failedTweetCount > 0 && (
            <Button
              danger
              icon={<CloseCircleOutlined />}
              onClick={onOpenFailedDrawer}
            >
              查看失败项 ({failedTweetCount})
            </Button>
          )}
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={onOpenCreateModal}
          >
            新建任务
          </Button>
        </Space>
      </Flex>

      {/* 过滤筛选工具栏 (纯 Ant Design 交互控件) */}
      <Card
        styles={{ body: { padding: "12px 16px" } }}
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-3 flex-wrap">
          {/* 状态分类 Segmented：括号内均为当前页计数，不是全局统计 */}
          <Tooltip title="括号内为当前页的任务计数，全局统计见侧边栏">
            <Segmented
              value={statusFilter}
              onChange={(val) => setStatusFilter(val as StatusFilterType)}
              options={[
                { label: `本页全部 (${jobs.length})`, value: "all" },
                {
                  label: (
                    <Space orientation="horizontal" size={4}>
                      {bucketCounts.active > 0 && <Badge status="processing" />}
                      <span>本页下载中 ({bucketCounts.active})</span>
                    </Space>
                  ),
                  value: "active",
                },
                { label: `本页已完成 (${bucketCounts.completed})`, value: "completed" },
                {
                  label: (
                    <Space orientation="horizontal" size={4}>
                      {bucketCounts.failed > 0 && <Badge status="error" />}
                      <span>本页失败 / 异常 ({bucketCounts.failed})</span>
                    </Space>
                  ),
                  value: "failed",
                },
              ]}
            />
          </Tooltip>

          {/* 搜索、类型选择与重试操作 */}
          <Space size={10} wrap>
            <Input.Search
              placeholder="按标题、用户名或任务 ID 搜索..."
              value={searchKeyword}
              onChange={(e) => setSearchKeyword(e.target.value)}
              allowClear
              style={{ width: 240 }}
            />

            <Select
              value={kindFilter}
              onChange={setKindFilter}
              style={{ width: 150 }}
              options={[
                { value: "all", label: "全部任务类型" },
                { value: "tweet_link", label: "单条推文" },
                { value: "user", label: "用户媒体" },
                { value: "list", label: "列表媒体" },
                { value: "following", label: "关注媒体" },
                { value: "failed_retry", label: "重试任务" },
              ]}
            />

            {bucketCounts.failed > 0 && (
              <Button
                icon={<RetweetOutlined />}
                loading={retryAllFailed.isPending}
                onClick={() => retryAllFailed.mutate()}
              >
                重试失败
              </Button>
            )}
          </Space>
        </div>
      </Card>

      {/* 任务核心数据表格 (纯 Ant Design Table) */}
      <Card
        style={{ overflow: "hidden" }}
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
            current: hasActiveFilter ? 1 : pagination.page,
            pageSize: pagination.pageSize,
            total: hasActiveFilter ? filteredJobs.length : pagination.total,
            showSizeChanger: true,
            pageSizeOptions: ["10", "20", "50", "100"],
            onChange: (page, pageSize) => {
              if (pageSize !== pagination.pageSize) {
                onPageSizeChange(pageSize);
              } else {
                onPageChange(page);
              }
            },
            showTotal: (total) =>
              hasActiveFilter
                ? `本页命中 ${total} 个任务（筛选仅作用于当前页，第 ${pagination.page} 页）`
                : `共 ${total} 个任务`,
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

  return (
    <div className="p-4 rounded-lg space-y-3 m-2" style={{ background: "var(--app-surface-muted)", border: "1px solid var(--app-border)" }}>
      <div className="flex items-center justify-between flex-wrap gap-2">
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
          <Spin />
          <Typography.Text type="secondary" className="block mt-2" style={{ fontSize: 12 }}>正在获取已下载媒体文件清单...</Typography.Text>
        </div>
      ) : filesQuery.isError ? (
        // 请求失败与“任务没有文件”是两回事，必须区分提示，否则无法判断该不该重试。
        <Alert
          type="error"
          showIcon
          message="读取文件清单失败"
          description={getErrorMessage(filesQuery.error)}
          action={
            <Button size="small" onClick={() => void filesQuery.refetch()}>
              重试
            </Button>
          }
        />
      ) : downloads.length === 0 && failed.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无已归档的文件记录" />
      ) : (
        <div className="grid grid-cols-2 sm:grid-cols-4 md:grid-cols-6 gap-2 pt-2" style={{ borderTop: "1px solid var(--app-border)" }}>
          {downloads.map((dl) => {
            const fileName = dl.filePath.split("/").pop() || dl.filePath;
            const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
            return (
              <Card
                key={dl.id}
                size="small"
                styles={{ body: { padding: "8px 10px" } }}
              >
                <div className="flex items-center justify-between gap-1">
                  <div className="flex items-center gap-1.5 overflow-hidden">
                    <Tag className="!m-0 font-mono" style={{ fontSize: 10 }}>
                      {ext}
                    </Tag>
                    <Typography.Text
                      ellipsis
                      style={{ fontSize: 11 }}
                    >
                      {fileName}
                    </Typography.Text>
                  </div>
                  <Tooltip title="复制文件本地完整路径">
                    <Button
                      type="text"
                      size="small"
                      icon={<CopyOutlined />}
                      onClick={() => void copyToClipboard(dl.filePath, "本地路径")}
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
              style={{ background: "#fef2f2", borderColor: "#fecaca" }}
              styles={{ body: { padding: "8px 10px" } }}
            >
              <Typography.Text ellipsis type="danger" className="block" style={{ fontSize: 11 }}>
                下载失败
              </Typography.Text>
              <Typography.Text ellipsis type="secondary" className="block" style={{ fontSize: 10 }}>
                {fl.error || fl.mediaUrl}
              </Typography.Text>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
