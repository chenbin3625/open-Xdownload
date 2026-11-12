import { t, useTranslation } from "../lib/i18n";
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
  Search,
  Sparkles,
  XCircle,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { Button, Card, Input, RadioGroup, Select, Typography } from "@douyinfe/semi-ui";
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
import { Tabs } from "../components/semi";
import { Alert, Descriptions, Empty, Progress } from "../components/semi";
import { Tooltip } from "../components/semi";
import { Pagination } from "../components/semi";
import { LoadingOverlay, Spinner } from "../components/semi";
import { Table, type TableColumn } from "../components/semi";
import { Avatar, StatusDot, Tag, type Tone } from "../components/semi";
import { toast } from "../components/semi";
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

function getJobStatusPresentation(status: Job["status"]) {
  const jobStatusPresentation: Record<
    Job["status"],
    { label: string; tone: Tone; icon?: React.ReactNode }
  > = {
    pending: { label: t("排队中"), tone: "default", icon: <Clock className="size-3" /> },
    resolving: { label: t("解析中"), tone: "brand", icon: <RefreshCw className="size-3 animate-spin" /> },
    downloading: { label: t("下载中"), tone: "brand", icon: <RefreshCw className="size-3 animate-spin" /> },
    completed: { label: t("已完成"), tone: "success", icon: <CheckCircle2 className="size-3" /> },
    completed_with_errors: {
      label: t("部分失败"),
      tone: "warning",
      icon: <AlertTriangle className="size-3" />,
    },
    failed: { label: t("失败"), tone: "danger", icon: <XCircle className="size-3" /> },
    canceled: { label: t("已取消"), tone: "default" },
  };

  const unknownStatusPresentation: { label: string; tone: Tone; icon: React.ReactNode } = {
    label: t("未知"),
    tone: "default",
    icon: <HelpCircle className="size-3" />,
  };

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
  useTranslation();
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
      toast.success({ message: t("任务已取消") });
    },
    onError: notifyError(t("取消失败")),
  });

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("已创建重试任务 #{{value0}}", { value0: job.id }) });
    },
    onError: notifyError(t("重试失败")),
  });

  const retryAllFailed = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (newJob) => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({
        message: t("已创建重试任务"),
        description: newJob.title || t("失败推文已重新加入执行队列"),
      });
    },
    onError: notifyError(t("重试失败")),
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
          type="danger"
          loading={cancel.isPending && cancel.variables === record.id}
          disabled={cancel.isPending && cancel.variables === record.id}
          aria-busy={cancel.isPending && cancel.variables === record.id}
          onClick={() => cancel.mutate(record.id)}
        >
          {t("取消")}</Button>
      )}
      {retryableStatuses.includes(record.status) && (
        <Button
          type="tertiary"
          loading={retry.isPending && retry.variables === record.id}
          aria-busy={retry.isPending && retry.variables === record.id}
          disabled={retry.isPending}
          onClick={() => retry.mutate(record.id)}
        >
          {t("重试")}</Button>
      )}
    </div>
  );

  const columns: TableColumn<Job>[] = [
    {
      key: "title",
      title: t("任务目标 / 描述"),
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
              {t("目标: ")}{record.input} · #JOB-{record.id}
            </div>
          </div>
        </div>
      ),
    },
    {
      key: "kind",
      title: t("类型"),
      width: 100,
      render: (record) => (
        <Tag tone="default">
          {kindLabel(record.kind)}
        </Tag>
      ),
    },
    {
      key: "status",
      title: t("当前状态"),
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
      title: t("执行进度"),
      width: 220,
      render: (record) => <JobProgress job={record} />,
    },
    {
      key: "updatedAt",
      title: t("更新时间"),
      width: 140,
      render: (record) => (
        <span className="font-mono text-xs text-fg-subtle">
          {formatDateTime(record.updatedAt)}
        </span>
      ),
    },
    {
      key: "action",
      title: t("操作"),
      width: 130,
      align: "right",
      render: renderJobActions,
    },
  ];

  const paginationFooter = (
    <div className="flex min-w-0 flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <span className="text-xs text-fg-muted">
        {hasActiveFilter
          ? t("本页匹配 {{value0}} 条（筛选仅作用于当前页，共 {{value1}} 个任务）", { value0: filteredJobs.length, value1: pagination.total })
          : t("共 {{value0}} 个下载任务", { value0: pagination.total })}
      </span>
      <Pagination
        page={pagination.page}
        pageSize={pagination.pageSize}
        total={pagination.total}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
      />
    </div>
  );

  return (
    <div className="workspace-page">
      <div className="page-heading">
        <h1 className="page-title">{t("任务调度中心")}</h1>
        <div className="page-actions">
          {failedTweetCount > 0 && (
            <Button
              type="danger"
              icon={<AlertCircle className="size-4" />}
              onClick={onOpenFailedDrawer}
            >
              {t("失败记录 (")}{failedTweetCount})
            </Button>
          )}
          <Button
            theme="solid"
            icon={<Plus className="size-4" />}
            onClick={onOpenCreateModal}
          >
            {t("新建任务")}</Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Card bodyStyle={{ padding: 16 }}>
          <div className="flex items-center justify-between gap-2 text-sm text-fg-muted">
            <span>{t("下载中任务")}</span>
            <StatusDot tone={displayStats.active > 0 ? "brand" : "default"} pulse={displayStats.active > 0} />
          </div>
          <div className="mt-2 text-2xl font-semibold tabular-nums">{displayStats.active}</div>
        </Card>
        <Card bodyStyle={{ padding: 16 }}>
          <div className="flex items-center justify-between gap-2 text-sm text-fg-muted">
            <span>{t("已完成")}</span><CheckCircle2 size={16} className="text-success" />
          </div>
          <div className="mt-2 text-2xl font-semibold tabular-nums">{displayStats.completed}</div>
        </Card>
        <Card bodyStyle={{ padding: 16 }}>
          <div className="flex items-center justify-between gap-2 text-sm text-fg-muted">
            <span>{t("异常 / 失败")}</span><AlertCircle size={16} className="text-danger" />
          </div>
          <div className="mt-2 text-2xl font-semibold tabular-nums">{displayStats.failed}</div>
        </Card>
        <Card bodyStyle={{ padding: 16 }}>
          <div className="flex items-center justify-between gap-2 text-sm text-fg-muted">
            <span>{t("任务总数")}</span><FolderCheck size={16} />
          </div>
          <div className="mt-2 text-2xl font-semibold tabular-nums">{displayStats.total}</div>
        </Card>
      </div>

      {/* 过滤筛选工具栏 */}
      <div className="page-toolbar">
        <Tooltip title={t("括号内为当前页的任务计数，全局统计见侧边栏与上方卡片")}>
          <div className="toolbar-status" role="radiogroup" aria-label={t("当前状态")}>
            <RadioGroup
              type="button"
              buttonSize="middle"
              value={statusFilter}
              onChange={(event) => changeStatusFilter(event.target.value as StatusFilterType)}
              options={[
                { label: t("全部 ({{value0}})", { value0: jobs.length }), value: "all" },
                {
                  label: (
                    <span className="flex items-center gap-1.5">
                      {bucketCounts.active > 0 && <span className="size-1.5 rounded-full bg-brand-500 animate-pulse" />}
                      <span>{t("下载中 (")}{bucketCounts.active})</span>
                    </span>
                  ),
                  value: "active",
                },
                { label: t("已完成 ({{value0}})", { value0: bucketCounts.completed }), value: "completed" },
                {
                  label: (
                    <span className="flex items-center gap-1.5">
                      {bucketCounts.failed > 0 && <span className="size-1.5 rounded-full bg-danger" />}
                      <span>{t("失败/异常 (")}{bucketCounts.failed})</span>
                    </span>
                  ),
                  value: "failed",
                },
              ]}
            />
          </div>
        </Tooltip>

        <div className="toolbar-filters">
          <Input
            prefix={<Search size={16} />}
            aria-label={t("按标题、用户名、错误信息或 ID 搜索...")}
            placeholder={t("按标题、用户名、错误信息或 ID 搜索...")}
            value={searchKeyword}
            onChange={setSearchKeyword}
            showClear
            className="toolbar-search"
          />

          <Select
            value={kindFilter}
            onChange={(value) => setKindFilter(String(value))}
            className="toolbar-select"
            aria-label={t("任务类型")}
            optionList={[
              { value: "all", label: t("全部任务类型") },
              { value: "tweet_link", label: t("单条推文") },
              { value: "user", label: t("用户媒体") },
              { value: "list", label: t("列表媒体") },
              { value: "following", label: t("关注媒体") },
              { value: "failed_retry", label: t("重试任务") },
            ]}
          />

          {/* 该按钮走失败推文队列的重试接口，只看队列计数，与失败任务数无关 */}
          {failedTweetCount > 0 && (
            <Button
              type="tertiary"
              icon={<RotateCcw className="size-4" />}
              loading={retryAllFailed.isPending}
              disabled={retryAllFailed.isPending}
              aria-busy={retryAllFailed.isPending}
              onClick={() => retryAllFailed.mutate()}
            >
              {t("重试失败推文")}</Button>
          )}
        </div>
      </div>

      {/* 任务核心数据表格 */}
      {isMobile ? (
        <div className="min-w-0 space-y-4">
          <LoadingOverlay loading={tableLoading}>
            {filteredJobs.length === 0 ? (
              <Empty description={t("暂无数据")} />
            ) : (
              <ul aria-label={t("任务列表")} className="space-y-3">
                {filteredJobs.map((job) => (
                  <MobileJobCard
                    key={job.id}
                    job={job}
                    actions={renderJobActions(job)}
                    onRetry={() => retry.mutate(job.id)}
                    retryingJobId={retry.isPending ? retry.variables : undefined}
                  />
                ))}
              </ul>
            )}
          </LoadingOverlay>
          {paginationFooter}
        </div>
      ) : (
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
                retryingJobId={retry.isPending ? retry.variables : undefined}
              />
            )}
            footer={paginationFooter}
          />
      )}
    </div>
  );
}

function JobProgress({ job }: { job: Job }) {
  useTranslation();
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

function MobileJobCard({
  job,
  actions,
  onRetry,
  retryingJobId,
}: {
  job: Job;
  actions: React.ReactNode;
  onRetry: () => void;
  retryingJobId?: number;
}) {
  useTranslation();
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
            theme="borderless"
            type="tertiary"
            aria-label={expanded ? t("收起详情") : t("展开详情")}
            aria-expanded={expanded}
            aria-controls={expanded ? panelId : undefined}
            onClick={() => setExpanded(!expanded)}
            icon={<ChevronDown className={cn("size-4 transition-transform", expanded && "rotate-180")} />}
          >
            {expanded ? t("收起详情") : t("查看详情")}
          </Button>
          {actions}
        </div>
      </div>
      {expanded && (
        <div id={panelId} className="border-t border-line">
          <ExpandedJobDetails job={job} onRetry={onRetry} retryingJobId={retryingJobId} />
        </div>
      )}
    </li>
  );
}

function JobErrorAlert({ job }: { job: Job }) {
  const { i18n } = useTranslation();
  const groups = useMemo(() => parseAggregatedErrors(job.error), [job.error, i18n.resolvedLanguage]);
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
      t("任务 ID: #{{value0}}", { value0: job.id }),
      t("任务状态: {{value0}}", { value0: job.status }),
      job.message ? t("状态描述: {{value0}}", { value0: job.message }) : "",
      job.error ? t("错误详情:\n{{value0}}", { value0: job.error }) : "",
    ]
      .filter(Boolean)
      .join("\n");

    if (!(await writeClipboard(textToCopy))) {
      toast.warning({
        message: t("复制失败"),
        description: t("当前浏览器环境不支持自动复制，请手动复制错误信息"),
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

  const title = job.status === "failed" ? t("任务执行失败") : t("任务处理存在异常或部分失败");

  return (
    <Alert
      type="error"
      showIcon
      className="border-danger/30"
      message={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span>{title}</span>
          <Button
            theme="borderless"
            type="danger"
            onClick={() => void handleCopy()}
            icon={copied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
          >
            {copied ? t("已复制诊断信息") : t("复制错误信息")}
          </Button>
        </div>
      }
      description={
        <div className="mt-2 space-y-2.5">
          {groups.length === 0 ? (
            <div className="font-mono text-xs text-danger font-medium break-all">
              {job.error || job.message || t("未知异常，任务未能成功完成")}
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
                          {group.count} {group.countUnit ? t("种异常") : t("个失败")}
                        </Tag>
                      </div>
                    </div>

                    {group.targets.length > 0 && (
                      <div className="space-y-1.5 pt-1.5 border-t border-line/50">
                        <div className="flex items-center justify-between text-[11px] text-fg-muted">
                          <span>{t("受影响账号 (")}{group.targets.length}):</span>
                          {hasMoreTargets && (
                            <button
                              type="button"
                              aria-expanded={isExpanded}
                              onClick={() => toggleExpand(group.id)}
                              className="text-[11px] text-brand-600 hover:underline cursor-pointer"
                            >
                              {isExpanded ? t("收起") : t("展开更多 (+{{value0}})", { value0: group.targets.length - 8 })}
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
                              {t("另有 ")}{hiddenCount} {t(" 个账号（相同原因已聚合）")}</Tag>
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
              <span className="shrink-0 font-medium text-fg-subtle">{t("状态概况:")}</span>
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
  retryingJobId,
}: {
  job: Job;
  onRetry: () => void;
  retryingJobId?: number;
}) {
  useTranslation();
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
      itemName={t("个文件")}
      emptyDescription={t("暂无下载文件记录")}
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
              <Tooltip title={t("复制文件完整本地路径")}>
                <Button
                  theme="borderless"
                  type="tertiary"
                  icon={<Copy className="size-3.5" />}
                  onClick={() => void copyToClipboard(item.filePath, t("本地路径"))}
                  aria-label={t("复制本地路径")}
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
      itemName={t("条失败记录")}
      emptyDescription={t("暂无失败媒体记录")}
      renderItem={(item) => (
        <div className="flex items-center justify-between gap-3 text-xs">
          <div className="flex min-w-0 flex-1 items-start gap-2.5">
            <div className="flex size-7 shrink-0 items-center justify-center rounded bg-danger-soft text-danger">
              <AlertCircle className="size-4" />
            </div>
            <div className="min-w-0 flex-1 space-y-0.5">
              <div className="font-medium text-danger break-all">
                {item.error || t("媒体下载失败")}
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
            <Tooltip title={t("复制媒体地址")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<Copy className="size-3.5" />}
                onClick={() => void copyToClipboard(item.mediaUrl, t("媒体地址"))}
                aria-label={t("复制媒体地址")}
              />
            </Tooltip>
          </div>
        </div>
      )}
    />
  );

  return (
    <div className="space-y-4 bg-surface-muted/50 p-4">
      {/* 顶部元信息与操作栏 */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Descriptions
          columns={3}
          items={[
            {
              key: "id",
              label: t("任务 ID"),
              children: <span className="font-mono">#{job.id}</span>,
            },
            {
              key: "created",
              label: t("创建时间"),
              children: <span className="font-mono">{formatDateTime(job.createdAt)}</span>,
            },
            {
              key: "mediaCount",
              label: t("入库文件"),
              children: (
                <div className="flex items-center gap-2">
                  <Tag tone="success">
                    {t("成功 ")}{downloads.length}
                  </Tag>
                  {failed.length > 0 && (
                    <Tag tone="danger">
                      {t("失败 ")}{failed.length}
                    </Tag>
                  )}
                  {hasJobError && failed.length === 0 && (
                    <Tag tone="danger">
                      {t("异常")}</Tag>
                  )}
                </div>
              ),
            },
          ]}
        />
        <Typography.Text link={{ href: `/tweets?jobId=${job.id}` }}>{t("archive.title")}</Typography.Text>
        {retryableStatuses.includes(job.status) && (
          <Button
            type="tertiary"
            icon={<RotateCcw className="size-3.5" />}
            onClick={onRetry}
            loading={retryingJobId === job.id}
            aria-busy={retryingJobId === job.id}
            disabled={retryingJobId !== undefined}
          >
            {t("重试此任务")}</Button>
        )}
      </div>

      {/* 错误提示横幅：结构化聚合相同报错并展示受影响账号 */}
      {hasJobError && <JobErrorAlert job={job} />}

      {filesQuery.data?.truncated && (
        <Alert type="warning" message={t("文件清单过长，每类仅展示最近 5000 条记录")} />
      )}

      {/* 文件与媒体清单：恢复为结构清晰的列表 */}
      {filesQuery.isLoading ? (
        <div className="py-6 text-center">
          <Spinner className="size-5 mx-auto" />
          <p className="mt-2 text-xs text-fg-muted">{t("正在获取已下载媒体文件清单...")}</p>
        </div>
      ) : filesQuery.isError ? (
        <Alert
          type="error"
          message={t("读取文件清单失败")}
          description={getErrorMessage(filesQuery.error)}
          action={
            <Button type="tertiary" onClick={() => void filesQuery.refetch()}>
              {t("重试")}</Button>
          }
        />
      ) : downloads.length === 0 && failed.length === 0 ? (
        hasJobError ? (
          <Empty description={t("任务未生成已下载文件记录")} />
        ) : job.status === "pending" || job.status === "resolving" || job.status === "downloading" ? (
          <div className="py-6 text-center text-xs text-fg-muted">
            {t("任务正在执行中，等待媒体拉取入库...")}</div>
        ) : (
          <Empty description={t("暂无已归档的文件记录")} />
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
                    <span>{t("已下载文件")}</span>
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
                    <span className="text-danger">{t("失败媒体")}</span>
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
              {t("已下载文件 (")}{downloads.length})
            </span>
          </div>
          {renderDownloadsList()}
        </div>
      ) : (
        <div className="space-y-2 pt-2 border-t border-line">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-danger">
              {t("失败媒体 (")}{failed.length})
            </span>
          </div>
          {renderFailedList()}
        </div>
      )}
    </div>
  );
}
