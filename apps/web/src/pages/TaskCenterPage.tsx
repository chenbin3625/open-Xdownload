import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Clock,
  Copy,
  FolderCheck,
  Plus,
  RefreshCw,
  RotateCcw,
  Sparkles,
  XCircle,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { Segmented, Select } from "../components/ui/Controls";
import { Alert, Descriptions, Empty, Progress } from "../components/ui/Feedback";
import { SearchInput } from "../components/ui/Input";
import { Tooltip } from "../components/ui/Overlay";
import { Pagination } from "../components/ui/Pagination";
import { Spinner } from "../components/ui/Spinner";
import { Table, type TableColumn } from "../components/ui/Table";
import { Avatar, StatusDot, Tag, type Tone } from "../components/ui/Tag";
import { toast } from "../components/ui/Toast";

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

const jobStatusPresentation: Record<
  Job["status"],
  { label: string; tone: Tone; icon?: React.ReactNode }
> = {
  pending: { label: "排队中", tone: "default", icon: <Clock className="size-3" /> },
  resolving: { label: "解析中", tone: "brand", icon: <RefreshCw className="size-3 animate-spin" /> },
  downloading: { label: "下载中", tone: "brand", icon: <RefreshCw className="size-3 animate-spin" /> },
  completed: { label: "已完成", tone: "success", icon: <CheckCircle2 className="size-3" /> },
  completed_with_errors: {
    label: "部分失败",
    tone: "warning",
    icon: <AlertTriangle className="size-3" />,
  },
  failed: { label: "失败", tone: "danger", icon: <XCircle className="size-3" /> },
  canceled: { label: "已取消", tone: "default" },
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
      toast.success({ message: "任务已取消" });
    },
    onError: notifyError("取消失败"),
  });

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: `已创建重试任务 #${job.id}` });
    },
    onError: notifyError("重试失败"),
  });

  const retryAllFailed = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (newJob) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: "已创建重试任务",
        description: newJob.title || "失败推文已重新加入执行队列",
      });
    },
    onError: notifyError("重试失败"),
  });

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

  const hasActiveFilter =
    statusFilter !== "all" || kindFilter !== "all" || searchKeyword.trim() !== "";

  const bucketCounts = useMemo(() => {
    const counts = { active: 0, completed: 0, failed: 0 };
    for (const job of jobs) {
      const bucket = jobStatusBucket(job.status);
      if (bucket !== "idle") counts[bucket] += 1;
    }
    return counts;
  }, [jobs]);

  const columns: TableColumn<Job>[] = [
    {
      key: "title",
      title: "任务目标 / 描述",
      width: 280,
      render: (record) => (
        <div className="flex items-center gap-2.5 py-0.5">
          <Avatar>
            {record.kind === "user" ? "@" : "𝕏"}
          </Avatar>
          <div className="min-w-0 max-w-xs md:max-w-md">
            <div className="truncate text-sm font-medium text-fg">
              {record.title || kindLabel(record.kind)}
            </div>
            <div className="truncate font-mono text-xs text-fg-muted">
              目标: {record.input} · #JOB-{record.id}
            </div>
          </div>
        </div>
      ),
    },
    {
      key: "kind",
      title: "类型",
      width: 100,
      render: (record) => (
        <Tag tone="default">
          {kindLabel(record.kind)}
        </Tag>
      ),
    },
    {
      key: "status",
      title: "当前状态",
      width: 120,
      render: (record) => {
        const meta = jobStatusPresentation[record.status];
        return (
          <Tag tone={meta.tone} icon={meta.icon}>
            {meta.label}
          </Tag>
        );
      },
    },
    {
      key: "progress",
      title: "执行进度",
      width: 220,
      render: (record) => {
        const percent = clampPercent(record.progress);
        const pStatus = progressStatus(record);
        return (
          <div className="space-y-1 py-0.5">
            <div className="flex justify-between font-mono text-xs text-fg-muted">
              <span className="truncate max-w-[130px]">{record.message || "执行中..."}</span>
              <span className="font-semibold text-brand-600 dark:text-brand-400">{percent}%</span>
            </div>
            <Progress
              percent={percent}
              status={
                pStatus === "active"
                  ? "active"
                  : pStatus === "success"
                  ? "success"
                  : pStatus === "exception"
                  ? "exception"
                  : "normal"
              }
            />
          </div>
        );
      },
    },
    {
      key: "updatedAt",
      title: "更新时间",
      width: 140,
      render: (record) => (
        <span className="font-mono text-xs text-fg-subtle">
          {formatDateTime(record.updatedAt)}
        </span>
      ),
    },
    {
      key: "action",
      title: "操作",
      width: 130,
      align: "right",
      render: (record) => {
        const canCancel = cancelableStatuses.includes(record.status);
        const canRetry = retryableStatuses.includes(record.status);
        const isCanceling = cancel.isPending && cancel.variables === record.id;
        const isRetrying = retry.isPending && retry.variables === record.id;

        return (
          <div className="flex items-center justify-end gap-1.5">
            {canCancel && (
              <Button
                variant="danger"
                size="sm"
                loading={isCanceling}
                onClick={() => cancel.mutate(record.id)}
              >
                取消
              </Button>
            )}
            {canRetry && (
              <Button
                variant="default"
                size="sm"
                loading={isRetrying}
                onClick={() => retry.mutate(record.id)}
              >
                重试
              </Button>
            )}
          </div>
        );
      },
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      {/* 顶部标题与操作栏 */}
      <div className="flex flex-wrap items-center justify-between gap-4 pb-1">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">任务调度中心</h1>
          <p className="mt-0.5 text-xs text-fg-muted">
            监控所有已创建的推文与归档任务、过滤检索并查看下载文件记录
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2.5">
          {failedTweetCount > 0 && (
            <Button
              variant="danger"
              icon={<AlertCircle className="size-4" />}
              onClick={onOpenFailedDrawer}
            >
              失败记录 ({failedTweetCount})
            </Button>
          )}
          <Button
            variant="primary"
            icon={<Plus className="size-4" />}
            onClick={onOpenCreateModal}
          >
            新建下载 / 归档
          </Button>
        </div>
      </div>

      {/* 核心指标统计卡片 (现代玻璃风格) */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Card
          size="sm"
          className="relative overflow-hidden border-t-2 border-t-brand-500 bg-gradient-to-b from-brand-500/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">下载中任务</span>
            <div className="flex size-6.5 items-center justify-center rounded-lg bg-brand-500/10">
              <StatusDot tone={bucketCounts.active > 0 ? "brand" : "default"} pulse={bucketCounts.active > 0} />
            </div>
          </div>
          <div className="mt-2 text-3xl font-bold tracking-tight font-mono tabular-nums text-fg">
            {bucketCounts.active}
          </div>
          <p className="mt-0.5 text-[11px] text-fg-subtle">当前正在排队与拉取</p>
        </Card>

        <Card
          size="sm"
          className="relative overflow-hidden border-t-2 border-t-success bg-gradient-to-b from-success/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">已完成</span>
            <div className="flex size-6.5 items-center justify-center rounded-lg bg-success-soft">
              <CheckCircle2 className="size-3.5 text-success" />
            </div>
          </div>
          <div className="mt-2 text-3xl font-bold tracking-tight font-mono tabular-nums text-fg">
            {bucketCounts.completed}
          </div>
          <p className="mt-0.5 text-[11px] text-fg-subtle">媒体已全部入库</p>
        </Card>

        <Card
          size="sm"
          className="relative overflow-hidden border-t-2 border-t-danger bg-gradient-to-b from-danger/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">异常 / 失败</span>
            <div className="flex size-6.5 items-center justify-center rounded-lg bg-danger-soft">
              <AlertCircle className="size-3.5 text-danger" />
            </div>
          </div>
          <div className="mt-2 text-3xl font-bold tracking-tight font-mono tabular-nums text-fg">
            {bucketCounts.failed}
          </div>
          <p className="mt-0.5 text-[11px] text-fg-subtle">包含部分失败与致命失败</p>
        </Card>

        <Card
          size="sm"
          className="relative overflow-hidden border-t-2 border-t-line-strong bg-gradient-to-b from-surface-muted/50 to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">任务总数</span>
            <div className="flex size-6.5 items-center justify-center rounded-lg bg-surface-muted border border-line">
              <FolderCheck className="size-3.5 text-brand-500" />
            </div>
          </div>
          <div className="mt-2 text-3xl font-bold tracking-tight font-mono tabular-nums text-fg">
            {pagination.total}
          </div>
          <p className="mt-0.5 text-[11px] text-fg-subtle">库内已记录的任务总规模</p>
        </Card>
      </div>

      {/* 过滤筛选工具栏 */}
      <Card size="sm" bodyClassName="p-3">
        <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between flex-wrap">
          {/* 状态分类 Segmented */}
          <Tooltip title="括号内为当前页的任务计数，全局统计见侧边栏与上方卡片">
            <div>
              <Segmented
                value={statusFilter}
                onChange={(val) => setStatusFilter(val as StatusFilterType)}
                options={[
                  { label: `全部 (${jobs.length})`, value: "all" },
                  {
                    label: (
                      <span className="flex items-center gap-1.5">
                        {bucketCounts.active > 0 && <span className="size-1.5 rounded-full bg-brand-500 animate-pulse" />}
                        <span>下载中 ({bucketCounts.active})</span>
                      </span>
                    ),
                    value: "active",
                  },
                  { label: `已完成 (${bucketCounts.completed})`, value: "completed" },
                  {
                    label: (
                      <span className="flex items-center gap-1.5">
                        {bucketCounts.failed > 0 && <span className="size-1.5 rounded-full bg-danger" />}
                        <span>失败/异常 ({bucketCounts.failed})</span>
                      </span>
                    ),
                    value: "failed",
                  },
                ]}
              />
            </div>
          </Tooltip>

          {/* 搜索与类型选择 */}
          <div className="flex flex-wrap items-center gap-2">
            <SearchInput
              placeholder="按标题、用户名或 ID 搜索..."
              value={searchKeyword}
              onChange={(e) => setSearchKeyword(e.target.value)}
              allowClear
              className="w-56"
            />

            <Select
              value={kindFilter}
              onChange={setKindFilter}
              className="w-36"
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
                variant="default"
                size="sm"
                icon={<RotateCcw className="size-3.5" />}
                loading={retryAllFailed.isPending}
                onClick={() => retryAllFailed.mutate()}
              >
                重试全部失败
              </Button>
            )}
          </div>
        </div>
      </Card>

      {/* 任务核心数据表格 */}
      <Card size="sm" bodyClassName="p-0">
        <Table<Job>
          columns={columns}
          data={filteredJobs}
          rowKey={(r) => r.id}
          loading={tableLoading}
          minWidth={860}
          expandedRowRender={(record) => (
            <ExpandedJobDetails
              job={record}
              onRetry={() => retry.mutate(record.id)}
            />
          )}
          footer={
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs text-fg-muted">
                {hasActiveFilter
                  ? `本页匹配 ${filteredJobs.length} 条（筛选仅作用于当前第 ${pagination.page} 页）`
                  : `共 ${pagination.total} 个下载任务`}
              </span>
              <Pagination
                page={hasActiveFilter ? 1 : pagination.page}
                pageSize={pagination.pageSize}
                total={hasActiveFilter ? filteredJobs.length : pagination.total}
                onPageChange={(page) => onPageChange(page)}
                onPageSizeChange={(size) => onPageSizeChange(size)}
              />
            </div>
          }
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
    <div className="space-y-3 rounded-card border border-line bg-surface-muted/50 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Descriptions
          columns={3}
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
                <div className="flex items-center gap-2">
                  <Tag tone="success">
                    成功 {downloads.length}
                  </Tag>
                  {failed.length > 0 && (
                    <Tag tone="danger">
                      失败 {failed.length}
                    </Tag>
                  )}
                </div>
              ),
            },
          ]}
        />
        {failed.length > 0 && (
          <Button variant="text" size="sm" onClick={onRetry}>
            重试此任务
          </Button>
        )}
      </div>

      {filesQuery.isLoading ? (
        <div className="py-6 text-center">
          <Spinner className="size-5 mx-auto" />
          <p className="mt-2 text-xs text-fg-muted">正在获取已下载媒体文件清单...</p>
        </div>
      ) : filesQuery.isError ? (
        <Alert
          type="error"
          message="读取文件清单失败"
          description={getErrorMessage(filesQuery.error)}
          action={
            <Button size="sm" variant="default" onClick={() => void filesQuery.refetch()}>
              重试
            </Button>
          }
        />
      ) : downloads.length === 0 && failed.length === 0 ? (
        <Empty description="暂无已归档的文件记录" />
      ) : (
        <div className="grid grid-cols-2 gap-2 pt-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 border-t border-line">
          {downloads.map((dl) => {
            const fileName = dl.filePath.split("/").pop() || dl.filePath;
            const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
            return (
              <div
                key={dl.id}
                className="flex items-center justify-between gap-1.5 rounded-control border border-line bg-surface p-2 text-xs shadow-xs"
              >
                <div className="flex min-w-0 items-center gap-1.5 overflow-hidden">
                  <span className="rounded bg-surface-muted px-1 py-0.5 font-mono text-[10px] text-fg-muted">
                    {ext}
                  </span>
                  <span className="truncate text-fg-body font-medium" title={fileName}>
                    {fileName}
                  </span>
                </div>
                <Tooltip title="复制文件本地完整路径">
                  <Button
                    variant="text"
                    size="sm"
                    circle
                    icon={<Copy className="size-3" />}
                    onClick={() => void copyToClipboard(dl.filePath, "本地路径")}
                    aria-label="复制本地路径"
                  />
                </Tooltip>
              </div>
            );
          })}

          {failed.map((fl) => (
            <div
              key={fl.id}
              className="rounded-control border border-danger/30 bg-danger-soft p-2 text-xs"
            >
              <span className="block font-medium text-danger truncate">下载失败</span>
              <span className="block text-[11px] text-fg-muted truncate" title={fl.error || fl.mediaUrl}>
                {fl.error || fl.mediaUrl}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
