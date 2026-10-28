import {
  AlertCircle,
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronDown,
  Clock,
  Copy,
  FolderCheck,
  HelpCircle,
  Plus,
  RefreshCw,
  RotateCcw,
  Sparkles,
  XCircle,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { getJobProgressDisplay, parseAggregatedErrors } from "../lib/jobErrors";
import {
  cancelJob,
  formatBytes,
  getJobFiles,
  jobFilesQueryRoot,
  retryFailedTweets,
  retryJob,
  type DashboardPagination,
  type DashboardStats,
  type DownloadRecord,
  type FailedMedia,
  type Job,
} from "../lib/api";
import {
  PaginatedList,
  clampPercent,
  copyToClipboard,
  formatDateTime,
  getErrorMessage,
  kindLabel,
  notifyError,
  writeClipboard,
} from "../components/common/CommonUI";
import {
  cancelableStatuses,
  jobStatusBucket,
  progressStatus,
  retryableStatuses,
} from "../lib/jobStatus";
import { invalidateWorkbenchQueries } from "../lib/useDashboardEvents";
import { useBreakpointUp } from "../lib/useMediaQuery";
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { Segmented, Select, Tabs } from "../components/ui/Controls";
import { Alert, Descriptions, Empty, Progress } from "../components/ui/Feedback";
import { SearchInput } from "../components/ui/Input";
import { Tooltip } from "../components/ui/Overlay";
import { Pagination } from "../components/ui/Pagination";
import { LoadingOverlay, Spinner } from "../components/ui/Spinner";
import { Table, type TableColumn } from "../components/ui/Table";
import { Avatar, StatusDot, Tag, type Tone } from "../components/ui/Tag";
import { toast } from "../components/ui/Toast";
import { cn } from "../lib/cn";

export type StatusFilterType = "all" | "active" | "completed" | "failed";

export interface TaskCenterPageProps {
  jobs: Job[];
  stats?: DashboardStats;
  downloads?: DownloadRecord[];
  failed?: FailedMedia[];
  failedTweetCount: number;
  pagination: DashboardPagination;
  tableLoading?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  onOpenCreateModal: () => void;
  onOpenFailedDrawer: () => void;
  /** 受控的状态筛选（App 从 URL 还原）；不传则内部自管。 */
  statusFilter?: StatusFilterType;
  onStatusFilterChange?: (status: StatusFilterType) => void;
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

const unknownStatusPresentation: { label: string; tone: Tone; icon: React.ReactNode } = {
  label: "未知",
  tone: "default",
  icon: <HelpCircle className="size-3" />,
};

function getJobStatusPresentation(status: Job["status"]) {
  return jobStatusPresentation[status] ?? unknownStatusPresentation;
}

export function TaskCenterPage({
  jobs,
  stats,
  failedTweetCount,
  pagination,
  tableLoading = false,
  onPageChange,
  onPageSizeChange,
  onOpenCreateModal,
  onOpenFailedDrawer,
  statusFilter: controlledStatusFilter,
  onStatusFilterChange,
}: TaskCenterPageProps) {
  const queryClient = useQueryClient();
  const isMobile = !useBreakpointUp("md");
  const [localStatusFilter, setLocalStatusFilter] = useState<StatusFilterType>("all");
  const statusFilter = controlledStatusFilter ?? localStatusFilter;
  // 筛选是当前页的客户端过滤，切筛选后回到第 1 页更符合直觉（页码也要写回 URL）。
  const changeStatusFilter = (status: StatusFilterType) => {
    if (controlledStatusFilter === undefined) setLocalStatusFilter(status);
    onStatusFilterChange?.(status);
    if (pagination.page > 1) onPageChange(1);
  };
  const [kindFilter, setKindFilter] = useState<string>("all");
  const [searchKeyword, setSearchKeyword] = useState<string>("");

  const isJobFailed = (job: Job) =>
    job.status === "failed" ||
    job.status === "completed_with_errors" ||
    Boolean(job.error);

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
      if (statusFilter === "active" && jobStatusBucket(job.status) !== "active") {
        return false;
      }
      if (statusFilter === "completed" && (job.status !== "completed" || Boolean(job.error))) {
        return false;
      }
      if (statusFilter === "failed" && !isJobFailed(job)) {
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
        String(job.id).includes(keyword) ||
        (job.error || "").toLowerCase().includes(keyword) ||
        (job.message || "").toLowerCase().includes(keyword)
      );
    });
  }, [jobs, statusFilter, kindFilter, searchKeyword]);

  const hasActiveFilter =
    statusFilter !== "all" || kindFilter !== "all" || searchKeyword.trim() !== "";

  const bucketCounts = useMemo(() => {
    const counts = { active: 0, completed: 0, failed: 0 };
    for (const job of jobs) {
      if (isJobFailed(job)) {
        counts.failed += 1;
      } else if (jobStatusBucket(job.status) === "active") {
        counts.active += 1;
      } else if (job.status === "completed") {
        counts.completed += 1;
      }
    }
    return counts;
  }, [jobs]);

  const displayStats = {
    active: stats ? stats.active : bucketCounts.active,
    completed: stats ? stats.completed : bucketCounts.completed,
    failed: stats ? stats.failed : bucketCounts.failed,
    total: stats ? stats.total : pagination.total,
  };

  const renderJobActions = (record: Job) => (
    <div className="flex items-center justify-end gap-1.5">
      {cancelableStatuses.includes(record.status) && (
        <Button
          variant="danger"
          size="sm"
          loading={cancel.isPending && cancel.variables === record.id}
          onClick={() => cancel.mutate(record.id)}
        >
          取消
        </Button>
      )}
      {retryableStatuses.includes(record.status) && (
        <Button
          variant="default"
          size="sm"
          loading={retry.isPending && retry.variables === record.id}
          onClick={() => retry.mutate(record.id)}
        >
          重试
        </Button>
      )}
    </div>
  );

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
        const meta = getJobStatusPresentation(record.status);
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
      render: (record) => <JobProgress job={record} />,
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
      render: renderJobActions,
    },
  ];

  const paginationFooter = (
    <div className="flex min-w-0 flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <span className="text-xs text-fg-muted">
        {hasActiveFilter
          ? `本页匹配 ${filteredJobs.length} 条（筛选仅作用于当前第 ${pagination.page} 页）`
          : `共 ${pagination.total} 个下载任务`}
      </span>
      <Pagination
        page={hasActiveFilter ? 1 : pagination.page}
        pageSize={pagination.pageSize}
        total={hasActiveFilter ? filteredJobs.length : pagination.total}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
      />
    </div>
  );

  return (
    <div className="flex flex-col gap-4">
      {/* 顶部标题与操作栏 */}
      <div className="flex flex-wrap items-center justify-between gap-4 pb-1">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">任务调度中心</h1>
          <p className="mt-0.5 hidden text-xs text-fg-muted sm:block">
            监控所有已创建的推文与归档任务、过滤检索并查看下载文件记录
          </p>
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:gap-2.5">
          {failedTweetCount > 0 && (
            <Button
              variant="danger"
              icon={<AlertCircle className="size-4" />}
              onClick={onOpenFailedDrawer}
              className="flex-1 sm:flex-none"
            >
              失败记录 ({failedTweetCount})
            </Button>
          )}
          <Button
            variant="primary"
            icon={<Plus className="size-4" />}
            onClick={onOpenCreateModal}
            className="flex-1 sm:flex-none"
          >
            新建任务
          </Button>
        </div>
      </div>

      {/* 核心指标统计卡片 (现代玻璃风格) */}
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 sm:gap-3">
        <Card
          size="sm"
          bodyClassName="p-3 sm:p-4"
          className="relative overflow-hidden border-t-2 border-t-brand-500 bg-gradient-to-b from-brand-500/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">下载中任务</span>
            <div className="hidden size-7 items-center justify-center rounded-lg bg-brand-500/10 sm:flex">
              <StatusDot tone={displayStats.active > 0 ? "brand" : "default"} pulse={displayStats.active > 0} />
            </div>
          </div>
          <div className="mt-2 text-2xl font-bold font-mono tabular-nums text-fg sm:text-3xl">
            {displayStats.active}
          </div>
          <p className="mt-0.5 hidden text-[11px] text-fg-subtle sm:block">当前正在排队与拉取</p>
        </Card>

        <Card
          size="sm"
          bodyClassName="p-3 sm:p-4"
          className="relative overflow-hidden border-t-2 border-t-success bg-gradient-to-b from-success/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">已完成</span>
            <div className="hidden size-7 items-center justify-center rounded-lg bg-success-soft sm:flex">
              <CheckCircle2 className="size-3.5 text-success" />
            </div>
          </div>
          <div className="mt-2 text-2xl font-bold font-mono tabular-nums text-fg sm:text-3xl">
            {displayStats.completed}
          </div>
          <p className="mt-0.5 hidden text-[11px] text-fg-subtle sm:block">媒体已全部入库</p>
        </Card>

        <Card
          size="sm"
          bodyClassName="p-3 sm:p-4"
          className="relative overflow-hidden border-t-2 border-t-danger bg-gradient-to-b from-danger/[0.04] to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">异常 / 失败</span>
            <div className="hidden size-7 items-center justify-center rounded-lg bg-danger-soft sm:flex">
              <AlertCircle className="size-3.5 text-danger" />
            </div>
          </div>
          <div className="mt-2 text-2xl font-bold font-mono tabular-nums text-fg sm:text-3xl">
            {displayStats.failed}
          </div>
          <p className="mt-0.5 hidden text-[11px] text-fg-subtle sm:block">包含部分失败与致命失败</p>
        </Card>

        <Card
          size="sm"
          bodyClassName="p-3 sm:p-4"
          className="relative overflow-hidden border-t-2 border-t-line-strong bg-gradient-to-b from-surface-muted/50 to-transparent hover:-translate-y-0.5 hover:shadow-raised transition-all duration-200 cursor-default"
        >
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">任务总数</span>
            <div className="hidden size-7 items-center justify-center rounded-lg bg-surface-muted border border-line sm:flex">
              <FolderCheck className="size-3.5 text-brand-500" />
            </div>
          </div>
          <div className="mt-2 text-2xl font-bold font-mono tabular-nums text-fg sm:text-3xl">
            {displayStats.total}
          </div>
          <p className="mt-0.5 hidden text-[11px] text-fg-subtle sm:block">库内已记录的任务总规模</p>
        </Card>
      </div>

      {/* 过滤筛选工具栏 */}
      <div className="min-w-0 border-y border-line py-3">
        <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between flex-wrap">
          {/* 状态分类 Segmented */}
          <Tooltip title="括号内为当前页的任务计数，全局统计见侧边栏与上方卡片">
            <div className="min-w-0 max-w-full">
              <Segmented
                size="md"
                value={statusFilter}
                onChange={(val) => changeStatusFilter(val as StatusFilterType)}
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
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <SearchInput
              placeholder="按标题、用户名、错误信息或 ID 搜索..."
              value={searchKeyword}
              onChange={(e) => setSearchKeyword(e.target.value)}
              allowClear
              containerClassName="w-full sm:w-56"
            />

            <Select
              value={kindFilter}
              onChange={setKindFilter}
              className="min-w-0 flex-1 sm:w-36 sm:flex-none"
              ariaLabel="任务类型"
              options={[
                { value: "all", label: "全部任务类型" },
                { value: "tweet_link", label: "单条推文" },
                { value: "user", label: "用户媒体" },
                { value: "list", label: "列表媒体" },
                { value: "following", label: "关注媒体" },
                { value: "failed_retry", label: "重试任务" },
              ]}
            />

            {/* 该按钮走失败推文队列的重试接口，只看队列计数，与失败任务数无关 */}
            {failedTweetCount > 0 && (
              <Button
                variant="default"
                size="md"
                icon={<RotateCcw className="size-4" />}
                loading={retryAllFailed.isPending}
                onClick={() => retryAllFailed.mutate()}
              >
                重试失败推文
              </Button>
            )}
          </div>
        </div>
      </div>

      {/* 任务核心数据表格 */}
      {isMobile ? (
        <div className="min-w-0 space-y-4">
          <LoadingOverlay loading={tableLoading}>
            {filteredJobs.length === 0 ? (
              <Empty description="暂无数据" />
            ) : (
              <ul aria-label="任务列表" className="space-y-3">
                {filteredJobs.map((job) => (
                  <MobileJobCard
                    key={job.id}
                    job={job}
                    actions={renderJobActions(job)}
                    onRetry={() => retry.mutate(job.id)}
                  />
                ))}
              </ul>
            )}
          </LoadingOverlay>
          {paginationFooter}
        </div>
      ) : (
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
            footer={paginationFooter}
          />
        </Card>
      )}
    </div>
  );
}

function JobProgress({ job }: { job: Job }) {
  const percent = clampPercent(job.progress);
  const status = progressStatus(job);
  const { displayMsg, tooltip, hasError } = getJobProgressDisplay(job);
  return (
    <div className="min-w-0 space-y-2 py-0.5">
      <div className="flex items-start justify-between gap-3 text-xs">
        <span
          className={cn(
            "min-w-0 flex-1 break-words md:truncate",
            hasError ? "font-medium text-danger" : "text-fg-muted",
          )}
          title={tooltip}
        >
          {displayMsg}
        </span>
        <span className={cn("shrink-0 font-mono font-semibold", hasError ? "text-danger" : "text-brand-600")}>
          {percent}%
        </span>
      </div>
      <Progress percent={percent} status={hasError ? "exception" : status} />
    </div>
  );
}

function MobileJobCard({ job, actions, onRetry }: { job: Job; actions: React.ReactNode; onRetry: () => void }) {
  const [expanded, setExpanded] = useState(false);
  const panelId = React.useId();
  const status = getJobStatusPresentation(job.status);
  return (
    <li className="min-w-0 overflow-hidden rounded-lg border border-line bg-surface">
      <div className="space-y-3 p-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <h2 className="break-words text-sm font-semibold text-fg">{job.title || kindLabel(job.kind)}</h2>
            <p className="mt-1 break-all font-mono text-xs text-fg-muted">{job.input}</p>
          </div>
          <Tag tone={status.tone} icon={status.icon}>{status.label}</Tag>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-fg-subtle">
          <Tag>{kindLabel(job.kind)}</Tag>
          <span className="font-mono">#JOB-{job.id}</span>
          <time className="ml-auto font-mono text-[11px]" dateTime={job.updatedAt}>{formatDateTime(job.updatedAt)}</time>
        </div>
        <JobProgress job={job} />
        <div className="flex items-center justify-between border-t border-line pt-2">
          <Button
            variant="text"
            size="sm"
            aria-label={expanded ? "收起详情" : "展开详情"}
            aria-expanded={expanded}
            aria-controls={expanded ? panelId : undefined}
            onClick={() => setExpanded(!expanded)}
            icon={<ChevronDown className={cn("size-4 transition-transform", expanded && "rotate-180")} />}
          >
            {expanded ? "收起详情" : "查看详情"}
          </Button>
          {actions}
        </div>
      </div>
      {expanded && (
        <div id={panelId} className="border-t border-line">
          <ExpandedJobDetails job={job} onRetry={onRetry} />
        </div>
      )}
    </li>
  );
}

function JobErrorAlert({ job }: { job: Job }) {
  const groups = useMemo(() => parseAggregatedErrors(job.error), [job.error]);
  const [copied, setCopied] = useState(false);
  const copiedTimerRef = useRef<number | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Record<string, boolean>>({});

  useEffect(
    () => () => {
      if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current);
    },
    [],
  );

  const toggleExpand = (id: string) => {
    setExpandedGroups((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  const handleCopy = async () => {
    const textToCopy = [
      `任务 ID: #${job.id}`,
      `任务状态: ${job.status}`,
      job.message ? `状态描述: ${job.message}` : "",
      job.error ? `错误详情:\n${job.error}` : "",
    ]
      .filter(Boolean)
      .join("\n");

    if (!(await writeClipboard(textToCopy))) {
      toast.warning({
        message: "复制失败",
        description: "当前浏览器环境不支持自动复制，请手动复制错误信息",
      });
      return;
    }
    setCopied(true);
    if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current);
    copiedTimerRef.current = window.setTimeout(() => {
      copiedTimerRef.current = null;
      setCopied(false);
    }, 2000);
  };

  const title = job.status === "failed" ? "任务执行失败" : "任务处理存在异常或部分失败";

  return (
    <Alert
      type="error"
      showIcon
      className="border-danger/30"
      message={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span>{title}</span>
          <Button
            size="sm"
            variant="ghost"
            className="h-6 px-2 text-[11px] text-danger hover:text-danger-600 hover:bg-danger/10"
            onClick={() => void handleCopy()}
            icon={copied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
          >
            {copied ? "已复制诊断信息" : "复制错误信息"}
          </Button>
        </div>
      }
      description={
        <div className="mt-2 space-y-2.5">
          {groups.length === 0 ? (
            <div className="font-mono text-xs text-danger font-medium break-all">
              {job.error || job.message || "未知异常，任务未能成功完成"}
            </div>
          ) : (
            <div className="space-y-2">
              {groups.map((group) => {
                const isExpanded = expandedGroups[group.id] ?? false;
                const visibleTargets = isExpanded ? group.targets : group.targets.slice(0, 8);
                const hasMoreTargets = group.targets.length > 8;
                const hiddenCount =
                  group.count > group.targets.length ? group.count - group.targets.length : 0;

                return (
                  <div
                    key={group.id}
                    className="rounded-card border border-danger/20 bg-surface/90 p-2.5 shadow-xs space-y-2"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <AlertCircle className="size-3.5 text-danger shrink-0" />
                        <span className="text-xs font-semibold text-danger break-all">
                          {group.reason}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 shrink-0">
                        {group.action && (
                          <Tag size="sm" tone="neutral">
                            {group.action}
                          </Tag>
                        )}
                        <Tag size="sm" tone="danger" className="font-medium">
                          {group.count} {group.countUnit ? "种异常" : "个失败"}
                        </Tag>
                      </div>
                    </div>

                    {group.targets.length > 0 && (
                      <div className="space-y-1.5 pt-1.5 border-t border-line/50">
                        <div className="flex items-center justify-between text-[11px] text-fg-muted">
                          <span>受影响账号 ({group.targets.length}):</span>
                          {hasMoreTargets && (
                            <button
                              type="button"
                              onClick={() => toggleExpand(group.id)}
                              className="text-[11px] text-brand-600 hover:underline cursor-pointer"
                            >
                              {isExpanded ? "收起" : `展开更多 (+${group.targets.length - 8})`}
                            </button>
                          )}
                        </div>
                        <div className="flex flex-wrap gap-1.5">
                          {visibleTargets.map((target) => (
                            <Tag
                              key={target}
                              size="sm"
                              tone="neutral"
                              className="font-mono text-fg-subtle bg-surface-muted/80 border-line/70"
                            >
                              {target}
                            </Tag>
                          ))}
                          {hiddenCount > 0 && (
                            <Tag size="sm" tone="warning" className="font-mono">
                              另有 {hiddenCount} 个账号（相同原因已聚合）
                            </Tag>
                          )}
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}

          {job.message && (
            <div className="flex min-w-0 flex-wrap items-start gap-1.5 border-t border-danger/15 pt-1 text-xs text-fg-muted">
              <span className="shrink-0 font-medium text-fg-subtle">状态概况:</span>
              <span className="min-w-0 break-all">{job.message}</span>
            </div>
          )}
        </div>
      }
    />
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
  const hasJobError =
    Boolean(job.error) ||
    job.status === "failed" ||
    job.status === "completed_with_errors";

  const [activeTab, setActiveTab] = useState<string>(
    failed.length > 0 && downloads.length === 0 ? "failed" : "downloads",
  );

  React.useEffect(() => {
    if (downloads.length === 0 && failed.length > 0) {
      setActiveTab("failed");
    } else if (downloads.length > 0 && failed.length === 0) {
      setActiveTab("downloads");
    }
  }, [downloads.length, failed.length]);

  const renderDownloadsList = () => (
    <PaginatedList<DownloadRecord>
      items={downloads}
      pageSize={6}
      itemName="个文件"
      emptyDescription="暂无下载文件记录"
      renderItem={(item) => {
        const fileName = item.filePath.split("/").pop() || item.filePath;
        const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
        return (
          <div className="flex items-center justify-between gap-3 text-xs">
            <div className="flex min-w-0 flex-1 items-start gap-2.5">
              <span className="flex size-7 shrink-0 items-center justify-center rounded bg-brand-500/10 font-mono text-[10px] font-bold text-brand-600">
                {ext}
              </span>
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex items-center gap-2">
                  <span className="truncate font-medium text-fg" title={fileName}>
                    {fileName}
                  </span>
                  {item.bytes > 0 && (
                    <span className="shrink-0 font-mono text-[11px] text-fg-subtle">
                      {formatBytes(item.bytes)}
                    </span>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-fg-muted">
                  <span
                    className="max-w-full truncate sm:max-w-md md:max-w-lg lg:max-w-xl text-fg-subtle"
                    title={item.filePath}
                  >
                    {item.filePath}
                  </span>
                  {item.mediaUrl && (
                    <span
                      className="max-w-full truncate sm:max-w-xs text-fg-subtle/70"
                      title={item.mediaUrl}
                    >
                      · {item.mediaUrl}
                    </span>
                  )}
                </div>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              <Tooltip title="复制文件完整本地路径">
                <Button
                  variant="ghost"
                  size="sm"
                  circle
                  icon={<Copy className="size-3.5" />}
                  onClick={() => void copyToClipboard(item.filePath, "本地路径")}
                  aria-label="复制本地路径"
                />
              </Tooltip>
            </div>
          </div>
        );
      }}
    />
  );

  const renderFailedList = () => (
    <PaginatedList<FailedMedia>
      items={failed}
      pageSize={6}
      itemName="条失败记录"
      emptyDescription="暂无失败媒体记录"
      renderItem={(item) => (
        <div className="flex items-center justify-between gap-3 text-xs">
          <div className="flex min-w-0 flex-1 items-start gap-2.5">
            <div className="flex size-7 shrink-0 items-center justify-center rounded bg-danger-soft text-danger">
              <AlertCircle className="size-4" />
            </div>
            <div className="min-w-0 flex-1 space-y-0.5">
              <div className="font-medium text-danger break-all">
                {item.error || "媒体下载失败"}
              </div>
              <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-fg-muted">
                <span
                  className="max-w-full truncate sm:max-w-md md:max-w-lg lg:max-w-xl text-fg-subtle"
                  title={item.mediaUrl}
                >
                  {item.mediaUrl}
                </span>
                {item.createdAt && (
                  <span className="text-fg-subtle/70">
                    · {formatDateTime(item.createdAt)}
                  </span>
                )}
              </div>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Tooltip title="复制媒体地址">
              <Button
                variant="ghost"
                size="sm"
                circle
                icon={<Copy className="size-3.5" />}
                onClick={() => void copyToClipboard(item.mediaUrl, "媒体地址")}
                aria-label="复制媒体地址"
              />
            </Tooltip>
          </div>
        </div>
      )}
    />
  );

  return (
    <div className="space-y-3 bg-surface-muted/50 p-3 md:rounded-card md:border md:border-line md:p-4">
      {/* 顶部元信息与操作栏 */}
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
                  {hasJobError && failed.length === 0 && (
                    <Tag tone="danger">
                      异常
                    </Tag>
                  )}
                </div>
              ),
            },
          ]}
        />
        {retryableStatuses.includes(job.status) && (
          <Button
            variant="secondary"
            size="sm"
            icon={<RotateCcw className="size-3.5" />}
            onClick={onRetry}
          >
            重试此任务
          </Button>
        )}
      </div>

      {/* 错误提示横幅：结构化聚合相同报错并展示受影响账号 */}
      {hasJobError && <JobErrorAlert job={job} />}

      {filesQuery.data?.truncated && (
        <Alert type="warning" message="文件清单过长，每类仅展示最近 5000 条记录" />
      )}

      {/* 文件与媒体清单：恢复为结构清晰的列表 */}
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
        hasJobError ? (
          <Empty description="任务未生成已下载文件记录" />
        ) : job.status === "pending" || job.status === "resolving" || job.status === "downloading" ? (
          <div className="py-6 text-center text-xs text-fg-muted">
            任务正在执行中，等待媒体拉取入库...
          </div>
        ) : (
          <Empty description="暂无已归档的文件记录" />
        )
      ) : downloads.length > 0 && failed.length > 0 ? (
        <div className="pt-2 border-t border-line">
          <Tabs
            value={activeTab}
            onChange={setActiveTab}
            items={[
              {
                key: "downloads",
                label: (
                  <span className="flex items-center gap-1.5">
                    <span>已下载文件</span>
                    <Tag tone="success" className="font-mono text-[10px] px-1 py-0">
                      {downloads.length}
                    </Tag>
                  </span>
                ),
                children: renderDownloadsList(),
              },
              {
                key: "failed",
                label: (
                  <span className="flex items-center gap-1.5">
                    <span className="text-danger">失败媒体</span>
                    <Tag tone="danger" className="font-mono text-[10px] px-1 py-0">
                      {failed.length}
                    </Tag>
                  </span>
                ),
                children: renderFailedList(),
              },
            ]}
          />
        </div>
      ) : downloads.length > 0 ? (
        <div className="space-y-2 pt-2 border-t border-line">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-fg-muted">
              已下载文件 ({downloads.length})
            </span>
          </div>
          {renderDownloadsList()}
        </div>
      ) : (
        <div className="space-y-2 pt-2 border-t border-line">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-danger">
              失败媒体 ({failed.length})
            </span>
          </div>
          {renderFailedList()}
        </div>
      )}
    </div>
  );
}
