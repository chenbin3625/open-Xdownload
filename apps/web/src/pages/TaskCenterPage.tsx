import { t, useTranslation } from "../lib/i18n";
import {
  Activity,
  AlertCircle,
  AlertTriangle,
  Check,
  CheckCircle2,
  Clock,
  Copy,
  FolderCheck,
  HelpCircle,
  Pin,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
  X,
  XCircle,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Card from "@douyinfe/semi-ui/lib/es/card";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import RadioGroup from "@douyinfe/semi-ui/lib/es/radio/radioGroup";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Typography from "@douyinfe/semi-ui/lib/es/typography";
import { getJobProgressDisplay, parseAggregatedErrors } from "../lib/jobErrors";
import {
  cancelJob,
  deleteJob,
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
  JobKindIcon,
  kindLabel,
  notifyError,
  writeClipboard,
} from "../components/common/CommonUI";
import {
  cancelableStatuses,
  isJobTerminal,
  jobStatusBucket,
  retryableStatuses,
} from "../lib/jobStatus";
import { invalidateWorkbenchQueries, removeJobFromCaches } from "../lib/useDashboardEvents";
import { useBreakpointUp } from "../lib/useMediaQuery";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { Drawer, Tooltip, Popconfirm } from "../components/semi/overlays";
import { Alert, Empty, LoadingOverlay, Spinner } from "../components/semi/feedback";
import { Pagination, Avatar, Tag, type Tone } from "../components/semi/data";
import { Table, type TableColumn } from "../components/semi/table";
import { Tabs } from "../components/semi/controls";
import { toast } from "../components/semi/toast";
import { cn } from "../lib/cn";

export type StatusFilterType = "all" | "active" | "completed" | "failed";

const jobKindFilterValues = [
  "tweet_link",
  "thread",
  "user",
  "list",
  "following",
  "bookmarks",
  "likes",
  "search",
  "failed_retry",
  "media_url",
  "asset_migration",
] as const;

function jobKindAvatarColor(kind: Job["kind"]) {
  const map: Record<string, "blue" | "indigo" | "cyan" | "amber" | "red" | "purple" | "green"> = {
    user: "blue",
    list: "indigo",
    following: "cyan",
    tweet_link: "amber",
    thread: "amber",
    bookmarks: "purple",
    likes: "red",
    search: "cyan",
    failed_retry: "red",
    media_url: "purple",
    asset_migration: "green",
  };
  return map[kind] ?? "blue";
}

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
  /** 受控的类型筛选；传入后由服务端分页筛选。 */
  kindFilter?: string;
  onKindFilterChange?: (kind: string) => void;
  /** 受控的关键词筛选；传入后由服务端分页筛选。 */
  searchKeyword?: string;
  onSearchKeywordChange?: (keyword: string) => void;
  onClearFilters?: () => void;
  filterCounts?: { all: number; active: number; completed: number; failed: number };
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

const pinFailedRetryStorageKey = "open_xdownload_pin_failed_retry";

function readPinFailedRetryPreference(): boolean {
  try {
    return typeof window !== "undefined" && window.localStorage?.getItem(pinFailedRetryStorageKey) === "1";
  } catch {
    return false;
  }
}

export const TaskCenterPage = React.memo(function TaskCenterPage({
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
  kindFilter: controlledKindFilter,
  onKindFilterChange,
  searchKeyword: controlledSearchKeyword,
  onSearchKeywordChange,
  onClearFilters,
  filterCounts,
}: TaskCenterPageProps) {
  const { i18n } = useTranslation();
  const queryClient = useQueryClient();
  const isMobile = !useBreakpointUp("xl");
  const [localStatusFilter, setLocalStatusFilter] = useState<StatusFilterType>("all");
  const statusFilter = controlledStatusFilter ?? localStatusFilter;
  // 受控时筛选条件由 App 交给服务端；未受控时保留本地筛选，便于独立使用和测试。
  const changeStatusFilter = (status: StatusFilterType) => {
    if (controlledStatusFilter === undefined) setLocalStatusFilter(status);
    onStatusFilterChange?.(status);
    if (pagination.page > 1) onPageChange(1);
  };
  const [localKindFilter, setLocalKindFilter] = useState<string>("all");
  const [localSearchKeyword, setLocalSearchKeyword] = useState<string>("");
  const kindFilter = controlledKindFilter ?? localKindFilter;
  const searchKeyword = controlledSearchKeyword ?? localSearchKeyword;
  const serverFiltering = controlledKindFilter !== undefined || controlledSearchKeyword !== undefined;
  // 输入框即时回显；300ms 防抖后才把关键词交给父组件（URL / 服务端查询）。
  const [searchInput, setSearchInput] = useState(searchKeyword);
  const debouncedSearchInput = useDebouncedValue(searchInput, 300);
  const lastSyncedSearchRef = useRef(searchKeyword);
  // 未受控时本页直接按输入过滤，保留原有"本页筛选"的即时体验。
  const filterKeyword = controlledSearchKeyword === undefined ? searchInput : searchKeyword;
  const changeKindFilter = (kind: string) => {
    if (controlledKindFilter === undefined) setLocalKindFilter(kind);
    onKindFilterChange?.(kind);
    if (pagination.page > 1) onPageChange(1);
  };
  // 程序化改动（清除筛选等）立即生效，不经过防抖。
  const changeSearchKeyword = (keyword: string) => {
    lastSyncedSearchRef.current = keyword;
    setSearchInput(keyword);
    if (controlledSearchKeyword === undefined) setLocalSearchKeyword(keyword);
    onSearchKeywordChange?.(keyword);
    if (pagination.page > 1) onPageChange(1);
  };

  const clearFilters = () => {
    lastSyncedSearchRef.current = "";
    setSearchInput("");
    if (onClearFilters) {
      if (controlledStatusFilter === undefined) setLocalStatusFilter("all");
      if (controlledKindFilter === undefined) setLocalKindFilter("all");
      if (controlledSearchKeyword === undefined) setLocalSearchKeyword("");
      onClearFilters();
      return;
    }
    changeSearchKeyword("");
    changeKindFilter("all");
    changeStatusFilter("all");
  };

  const pageRef = useRef(pagination.page);
  pageRef.current = pagination.page;
  const onPageChangeRef = useRef(onPageChange);
  onPageChangeRef.current = onPageChange;

  const externalSearchChanged = searchKeyword !== lastSyncedSearchRef.current;

  // 外部值（URL 回退、清除筛选）变化时同步回输入框；自己防抖发出的值不回灌，
  // 避免 flush 时覆盖用户已经继续输入的新内容。
  useEffect(() => {
    if (searchKeyword === lastSyncedSearchRef.current) return;
    lastSyncedSearchRef.current = searchKeyword;
    setSearchInput(searchKeyword);
  }, [searchKeyword]);

  useEffect(() => {
    if (externalSearchChanged) return;
    if (debouncedSearchInput !== searchInput) return;
    if (debouncedSearchInput === lastSyncedSearchRef.current) return;
    lastSyncedSearchRef.current = debouncedSearchInput;
    if (controlledSearchKeyword === undefined) setLocalSearchKeyword(debouncedSearchInput);
    onSearchKeywordChange?.(debouncedSearchInput);
    if (pageRef.current > 1) onPageChangeRef.current(1);
  }, [debouncedSearchInput, searchInput, controlledSearchKeyword, onSearchKeywordChange]);

  const [detailId, setDetailId] = useState<number | null>(null);
  const [detailSnapshot, setDetailSnapshot] = useState<Job | null>(null);
  const [retryCreatedJobId, setRetryCreatedJobId] = useState<number | null>(null);
  const [selectedJobIds, setSelectedJobIds] = useState<Set<number>>(new Set());
  const [cancelingJobIds, setCancelingJobIds] = useState<Set<number>>(() => new Set());
  const [removingJobIds, setRemovingJobIds] = useState<Set<number>>(() => new Set());
  const [pinFailedRetry, setPinFailedRetry] = useState<boolean>(() => readPinFailedRetryPreference());

  const togglePinFailedRetry = useCallback(() => {
    setPinFailedRetry(current => {
      const next = !current;
      try {
        window.localStorage?.setItem(pinFailedRetryStorageKey, next ? "1" : "0");
      } catch {
        // ignore storage errors in restricted environments
      }
      return next;
    });
  }, []);

  const detailIdRef = useRef(detailId);
  detailIdRef.current = detailId;
  const jobsRef = useRef(jobs);
  jobsRef.current = jobs;
  const tableLoadingRef = useRef(tableLoading);
  tableLoadingRef.current = tableLoading;
  const cancelingIdsRef = useRef(new Set<number>());
  const removingIdsRef = useRef(new Set<number>());

  const liveDetailJob = detailId === null ? undefined : jobs.find(job => job.id === detailId);
  const detailJob = liveDetailJob ?? (detailId !== null && detailSnapshot?.id === detailId ? detailSnapshot : undefined);

  useEffect(() => {
    if (liveDetailJob && liveDetailJob !== detailSnapshot) {
      setDetailSnapshot(liveDetailJob);
    }
  }, [liveDetailJob, detailSnapshot]);

  useEffect(() => {
    if (detailId !== null && !detailJob) {
      setDetailId(null);
      setDetailSnapshot(null);
      setRetryCreatedJobId(null);
    }
  }, [detailId, detailJob]);

  const openJobDetails = useCallback((id: number) => {
    const target = jobsRef.current.find(job => job.id === id) ?? null;
    setRetryCreatedJobId(null);
    setDetailSnapshot(target);
    setDetailId(target ? id : null);
  }, []);

  const closeJobDetails = useCallback(() => {
    setDetailId(null);
    setDetailSnapshot(null);
    setRetryCreatedJobId(null);
  }, []);

  const isJobFailed = (job: Job) =>
    job.status === "failed" ||
    job.status === "completed_with_errors";

  const filteredJobs = useMemo(() => {
    const keyword = filterKeyword.trim().toLowerCase();
    const base = serverFiltering
      ? jobs
      : jobs.filter((job) => {
          if (statusFilter === "active" && jobStatusBucket(job.status) !== "active") {
            return false;
          }
          if (statusFilter === "completed" && job.status !== "completed") {
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
    if (!pinFailedRetry) return base;
    const pinned: Job[] = [];
    const rest: Job[] = [];
    for (const job of base) {
      if (job.kind === "failed_retry") pinned.push(job);
      else rest.push(job);
    }
    return pinned.length > 0 ? [...pinned, ...rest] : base;
  }, [jobs, statusFilter, kindFilter, filterKeyword, serverFiltering, pinFailedRetry]);

  useEffect(() => {
    setSelectedJobIds(new Set());
  }, [pagination.page, pagination.pageSize, statusFilter, kindFilter, filterKeyword]);

  useEffect(() => {
    if (tableLoading) {
      setSelectedJobIds(current => (current.size === 0 ? current : new Set()));
      return;
    }
    setSelectedJobIds(current => {
      if (current.size === 0) return current;
      const visible = new Set(filteredJobs.map(job => job.id));
      const next = new Set([...current].filter(id => visible.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [filteredJobs, tableLoading]);

  const markCanceling = useCallback((ids: number[], active: boolean) => {
    ids.forEach(id => {
      if (active) cancelingIdsRef.current.add(id);
      else cancelingIdsRef.current.delete(id);
    });
    setCancelingJobIds(new Set(cancelingIdsRef.current));
  }, []);

  const markRemoving = useCallback((id: number, active: boolean) => {
    if (active) removingIdsRef.current.add(id);
    else removingIdsRef.current.delete(id);
    setRemovingJobIds(new Set(removingIdsRef.current));
  }, []);

  const cancel = useMutation({
    mutationFn: cancelJob,
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("任务已取消") });
    },
    onError: notifyError(t("取消失败")),
    onSettled: (_data, _error, jobId) => {
      markCanceling([jobId], false);
    },
  });

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job, retriedId) => {
      if (detailIdRef.current === retriedId) setRetryCreatedJobId(job.id);
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("已创建重试任务 #{{value0}}", { value0: job.id }), description: <a href={`/tasks?q=${job.id}`} className="text-brand-600">{t("查看新任务")}</a> });
    },
    onError: notifyError(t("重试失败")),
  });

  const bulkCancel = useMutation({
    mutationFn: async (ids: number[]) => {
      const results = await Promise.allSettled(ids.map(async (id) => ({ id, job: await cancelJob(id) })));
      const succeeded: number[] = [];
      const errors: string[] = [];
      for (const result of results) {
        if (result.status === "fulfilled") succeeded.push(result.value.id);
        else errors.push(getErrorMessage(result.reason));
      }
      return { succeeded, errors };
    },
    onSuccess: ({ succeeded, errors }) => {
      if (succeeded.length > 0) {
        setSelectedJobIds(current => {
          const next = new Set(current);
          succeeded.forEach(id => next.delete(id));
          return next;
        });
      }
      if (errors.length === 0) {
        toast.success({ message: t("已取消 {{value0}} 个任务", { value0: succeeded.length }) });
      } else if (succeeded.length > 0) {
        toast.warning({
          message: t("已取消 {{value0}} 个任务，{{value1}} 个失败", { value0: succeeded.length, value1: errors.length }),
          description: errors[0],
        });
      } else {
        toast.error({ message: t("批量取消失败"), description: errors[0] });
      }
    },
    onError: notifyError(t("批量取消失败")),
    onSettled: (_data, _error, ids) => {
      markCanceling(ids, false);
      void invalidateWorkbenchQueries(queryClient);
    },
  });

  const bulkRetry = useMutation({
    mutationFn: async (ids: number[]) => {
      const results = await Promise.allSettled(ids.map(async (id) => ({ id, job: await retryJob(id) })));
      const succeeded: number[] = [];
      const errors: string[] = [];
      for (const result of results) {
        if (result.status === "fulfilled") succeeded.push(result.value.id);
        else errors.push(getErrorMessage(result.reason));
      }
      return { succeeded, errors };
    },
    onSuccess: ({ succeeded, errors }) => {
      if (succeeded.length > 0) {
        setSelectedJobIds(current => {
          const next = new Set(current);
          succeeded.forEach(id => next.delete(id));
          return next;
        });
      }
      if (errors.length === 0) {
        toast.success({ message: t("已创建 {{value0}} 个重试任务", { value0: succeeded.length }) });
      } else if (succeeded.length > 0) {
        toast.warning({
          message: t("已创建 {{value0}} 个重试任务，{{value1}} 个失败", { value0: succeeded.length, value1: errors.length }),
          description: errors[0],
        });
      } else {
        toast.error({ message: t("批量重试失败"), description: errors[0] });
      }
    },
    onError: notifyError(t("批量重试失败")),
    onSettled: () => { void invalidateWorkbenchQueries(queryClient); },
  });

  const bulkDelete = useMutation({
    mutationFn: async (ids: number[]) => {
      const results = await Promise.allSettled(ids.map(async (id) => ({ id, result: await deleteJob(id) })));
      const succeeded: number[] = [];
      const errors: string[] = [];
      for (const result of results) {
        if (result.status === "fulfilled") succeeded.push(result.value.id);
        else errors.push(getErrorMessage(result.reason));
      }
      return { succeeded, errors };
    },
    onSuccess: ({ succeeded, errors }) => {
      if (succeeded.length > 0) {
        succeeded.forEach(id => {
          if (detailIdRef.current === id) closeJobDetails();
          removeJobFromCaches(queryClient, id);
        });
        setSelectedJobIds(current => {
          const next = new Set(current);
          succeeded.forEach(id => next.delete(id));
          return next;
        });
      }
      if (errors.length === 0) {
        toast.success({ message: t("已删除 {{value0}} 个任务", { value0: succeeded.length }) });
      } else if (succeeded.length > 0) {
        toast.warning({
          message: t("已删除 {{value0}} 个任务，{{value1}} 个失败", { value0: succeeded.length, value1: errors.length }),
          description: errors[0],
        });
      } else {
        toast.error({ message: t("批量删除失败"), description: errors[0] });
      }
    },
    onError: notifyError(t("批量删除失败")),
    onSettled: (_data, _error, ids) => {
      ids.forEach(id => markRemoving(id, false));
      void invalidateWorkbenchQueries(queryClient);
    },
  });

  const removeJob = useMutation({
    mutationFn: deleteJob,
    onSuccess: (_result, deletedId) => {
      if (detailIdRef.current === deletedId) {
        closeJobDetails();
      }
      setSelectedJobIds(current => {
        if (!current.has(deletedId)) return current;
        const next = new Set(current);
        next.delete(deletedId);
        return next;
      });
      removeJobFromCaches(queryClient, deletedId);
      void invalidateWorkbenchQueries(queryClient);
      toast.success({ message: t("任务已删除") });
    },
    onError: notifyError(t("删除失败")),
    onSettled: (_data, _error, deletedId) => {
      markRemoving(deletedId, false);
    },
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

  const handleCancelJob = useCallback((id: number) => {
    if (tableLoadingRef.current || cancelingIdsRef.current.has(id) || removingIdsRef.current.has(id)) return;
    markCanceling([id], true);
    cancel.mutate(id);
  }, [cancel.mutate, markCanceling]);

  const handleRemoveJob = useCallback((id: number) => {
    if (tableLoadingRef.current || removingIdsRef.current.has(id) || cancelingIdsRef.current.has(id)) return;
    markRemoving(id, true);
    removeJob.mutate(id);
  }, [markRemoving, removeJob.mutate]);

  const handleBulkCancel = useCallback((ids: number[]) => {
    if (tableLoadingRef.current) return;
    const eligible = ids.filter(id => !cancelingIdsRef.current.has(id) && !removingIdsRef.current.has(id));
    if (eligible.length === 0) return;
    markCanceling(eligible, true);
    bulkCancel.mutate(eligible);
  }, [bulkCancel.mutate, markCanceling]);

  const handleBulkRetry = useCallback((ids: number[]) => {
    if (tableLoadingRef.current || ids.length === 0) return;
    bulkRetry.mutate(ids);
  }, [bulkRetry.mutate]);

  const handleBulkDelete = useCallback((ids: number[]) => {
    if (tableLoadingRef.current) return;
    const eligible = ids.filter(id => !removingIdsRef.current.has(id) && !cancelingIdsRef.current.has(id));
    if (eligible.length === 0) return;
    eligible.forEach(id => markRemoving(id, true));
    bulkDelete.mutate(eligible);
  }, [bulkDelete.mutate, markRemoving]);

  const hasActiveFilter =
    statusFilter !== "all" || kindFilter !== "all" || filterKeyword.trim() !== "";

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

  const globalFilterCounts = filterCounts ?? {
    all: jobs.length,
    active: bucketCounts.active,
    completed: bucketCounts.completed,
    failed: bucketCounts.failed,
  };

  const selectedJobs = filteredJobs.filter(job => selectedJobIds.has(job.id));
  const cancelableSelectedIds = selectedJobs.filter(job => cancelableStatuses.includes(job.status)).map(job => job.id);
  const retryableSelectedIds = selectedJobs.filter(job => retryableStatuses.includes(job.status)).map(job => job.id);
  const deletableSelectedIds = selectedJobs.filter(job => isJobTerminal(job.status)).map(job => job.id);
  const allVisibleSelected = filteredJobs.length > 0 && filteredJobs.every(job => selectedJobIds.has(job.id));
  const filteredJobsRef = useRef(filteredJobs);
  filteredJobsRef.current = filteredJobs;
  const toggleSelected = useCallback((id: number, checked: boolean) => {
    if (tableLoadingRef.current) return;
    setSelectedJobIds(current => {
      const next = new Set(current);
      if (checked) next.add(id); else next.delete(id);
      return next;
    });
  }, []);
  const toggleAllVisible = useCallback((checked: boolean) => {
    if (tableLoadingRef.current) return;
    setSelectedJobIds(current => {
      const next = new Set(current);
      filteredJobsRef.current.forEach(job => checked ? next.add(job.id) : next.delete(job.id));
      return next;
    });
  }, []);
  const jobRowKey = useCallback((job: Job) => job.id, []);

  const displayStats = {
    active: stats ? stats.active : bucketCounts.active,
    completed: stats ? stats.completed : bucketCounts.completed,
    failed: stats ? stats.failed : bucketCounts.failed,
    total: stats ? stats.total : pagination.total,
  };

  // 把 mutation 的瞬时状态收敛成标量，列定义不必随 useMutation 返回的新对象每次重建。
  const retryPendingId = retry.isPending ? retry.variables : undefined;

  const renderJobActions = useCallback((record: Job, includeDetails = false) => {
    const isCanceling = cancelingJobIds.has(record.id);
    const isRemoving = removingJobIds.has(record.id);
    const isRowMutating = isCanceling || isRemoving;
    return (
      <div className="flex items-center justify-end gap-1.5">
        {includeDetails && (
          <Button
            theme="borderless"
            type="tertiary"
            icon={<FolderCheck className="size-4" />}
            aria-label={t("查看任务详情")}
            onClick={() => openJobDetails(record.id)}
          >
            {t("详情")}
          </Button>
        )}
        {cancelableStatuses.includes(record.status) && (
          <Button
            type="danger"
            loading={isCanceling}
            disabled={tableLoading || isRowMutating}
            aria-busy={isCanceling}
            onClick={() => handleCancelJob(record.id)}
          >
            {t("取消")}</Button>
        )}
        {retryableStatuses.includes(record.status) && (
          <Button
            type="tertiary"
            loading={retryPendingId === record.id}
            aria-busy={retryPendingId === record.id}
            disabled={tableLoading || retryPendingId !== undefined || isRowMutating}
            onClick={() => retry.mutate(record.id)}
          >
            {t("重试")}</Button>
        )}
        {isJobTerminal(record.status) && (
          <Popconfirm
            title={t("删除任务")}
            description={t("确定删除任务 #JOB-{{value0}}？该任务的下载索引会一并移除，磁盘上的媒体文件不受影响。", { value0: record.id })}
            okText={t("删除")}
            cancelText={t("取消")}
            onConfirm={() => handleRemoveJob(record.id)}
          >
            <Button
              theme="borderless"
              type="danger"
              icon={<Trash2 className="size-4 text-danger" />}
              loading={isRemoving}
              disabled={tableLoading || isRowMutating}
              aria-busy={isRemoving}
              aria-label={t("删除任务")}
            />
          </Popconfirm>
        )}
      </div>
    );
  }, [cancelingJobIds, handleCancelJob, handleRemoveJob, openJobDetails, removingJobIds, retry.mutate, retryPendingId, tableLoading]);

  const columns = useMemo<TableColumn<Job>[]>(() => [
    {
      key: "select",
      title: (
        <Checkbox
          aria-label={t("选择当前页任务")}
          checked={allVisibleSelected}
          indeterminate={selectedJobs.length > 0 && !allVisibleSelected}
          disabled={tableLoading || filteredJobs.length === 0}
          onChange={event => toggleAllVisible(event.target.checked === true)}
        />
      ),
      width: 42,
      render: record => (
        <Checkbox
          aria-label={t("选择任务 #{{value0}}", { value0: record.id })}
          checked={selectedJobIds.has(record.id)}
          disabled={tableLoading}
          onChange={event => toggleSelected(record.id, event.target.checked === true)}
        />
      ),
    },
    {
      key: "title",
      title: t("任务目标 / 描述"),
      render: (record) => <JobTitleCell job={record} pinned={pinFailedRetry && record.kind === "failed_retry"} onOpenDetails={openJobDetails} />,
    },
    {
      key: "kind",
      title: t("类型"),
      width: 120,
      render: (record) => <JobKindCell job={record} />,
    },
    {
      key: "status",
      title: t("当前状态"),
      width: 260,
      render: (record) => <JobProgress job={record} />,
    },
    {
      key: "updatedAt",
      title: t("更新时间"),
      width: 140,
      render: (record) => <JobUpdatedCell updatedAt={record.updatedAt} />,
    },
    {
      key: "action",
      title: t("操作"),
      /* 详情 + 取消/重试 + 删除，四枚 32px 控件在 148px 里会溢出。 */
      width: 212,
      align: "right",
      render: record => renderJobActions(record, true),
    },
  ], [allVisibleSelected, filteredJobs.length, i18n.resolvedLanguage, openJobDetails, pinFailedRetry, renderJobActions, selectedJobIds, selectedJobs.length, tableLoading, toggleAllVisible, toggleSelected]);

  const summaryLabel = hasActiveFilter
    ? serverFiltering
      ? t("匹配 {{value0}} 条任务", { value0: pagination.total })
      : t("本页匹配 {{value0}} 条（共 {{value1}} 个任务）", { value0: filteredJobs.length, value1: pagination.total })
    : t("共 {{value0}} 个下载任务", { value0: pagination.total });

  const paginationFooter = (
    <Pagination
      page={pagination.page}
      pageSize={pagination.pageSize}
      total={pagination.total}
      onPageChange={onPageChange}
      onPageSizeChange={onPageSizeChange}
      totalLabel={() => summaryLabel}
    />
  );

  return (
    <div className="workspace-page task-center">
      <div className="page-heading">
        <h1 className="page-title">{t("任务调度中心")}</h1>
        <div className="page-actions">
          {failedTweetCount > 0 && (
            <Button
              type="danger"
              icon={<AlertCircle className="size-4" />}
              aria-label={`${t("待重试推文")} (${failedTweetCount})`}
              title={`${t("待重试推文")} (${failedTweetCount})`}
              onClick={onOpenFailedDrawer}
            >
              <span className="task-failure-label">{t("待重试推文")} ({failedTweetCount})</span>
              <span className="task-failure-count" aria-hidden="true">{failedTweetCount}</span>
            </Button>
          )}
          {failedTweetCount > 0 && (
            <Button
              type="tertiary"
              icon={<RotateCcw className="size-4" />}
              loading={retryAllFailed.isPending}
              disabled={retryAllFailed.isPending}
              aria-busy={retryAllFailed.isPending}
              onClick={() => retryAllFailed.mutate()}
            >
              {t("重试失败推文")}
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

      <div className="task-stats">
        <button
          type="button"
          className="task-stat-button"
          aria-pressed={statusFilter === "active"}
          onClick={() => changeStatusFilter(statusFilter === "active" ? "all" : "active")}
        >
          <Card className={cn("task-stat task-stat-active", statusFilter === "active" && "task-stat-selected")}>
            <div className="task-stat-label">
              <span>{t("下载中任务")}</span>
              <span className="task-stat-icon bg-brand-50">
                <Activity size={15} className={displayStats.active > 0 ? "text-brand-700" : "text-fg-subtle"} />
              </span>
            </div>
            <div className="task-stat-value">{displayStats.active}</div>
          </Card>
        </button>
        <button
          type="button"
          className="task-stat-button"
          aria-pressed={statusFilter === "completed"}
          onClick={() => changeStatusFilter(statusFilter === "completed" ? "all" : "completed")}
        >
          <Card className={cn("task-stat task-stat-completed", statusFilter === "completed" && "task-stat-selected")}>
            <div className="task-stat-label">
              <span>{t("已完成")}</span>
              <span className="task-stat-icon bg-success-soft">
                <CheckCircle2 size={15} className="text-success" />
              </span>
            </div>
            <div className="task-stat-value">{displayStats.completed}</div>
          </Card>
        </button>
        <button
          type="button"
          className="task-stat-button"
          aria-pressed={statusFilter === "failed"}
          onClick={() => changeStatusFilter(statusFilter === "failed" ? "all" : "failed")}
        >
          <Card className={cn("task-stat task-stat-failed", statusFilter === "failed" && "task-stat-selected")}>
            <div className="task-stat-label">
              <span>{t("异常 / 失败")}</span>
              <span className="task-stat-icon bg-danger-soft">
                <AlertCircle size={15} className="text-danger" />
              </span>
            </div>
            <div className="task-stat-value">{displayStats.failed}</div>
          </Card>
        </button>
        <button
          type="button"
          className="task-stat-button"
          aria-pressed={statusFilter === "all"}
          onClick={() => changeStatusFilter("all")}
        >
          <Card className={cn("task-stat", statusFilter === "all" && "task-stat-selected")}>
            <div className="task-stat-label">
              <span>{t("任务总数")}</span>
              <span className="task-stat-icon bg-surface-muted">
                <FolderCheck size={15} className="text-fg-muted" />
              </span>
            </div>
            <div className="task-stat-value">{displayStats.total}</div>
          </Card>
        </button>
      </div>

      {/* 过滤筛选工具栏 */}
      <div className="page-toolbar">
        <div className="toolbar-row">
        <Tooltip title={serverFiltering ? t("计数来自全部任务，分页只影响当前列表") : t("括号内为当前页的任务计数")}>
          <div className="toolbar-status" role="radiogroup" aria-label={t("当前状态")}>
            <RadioGroup
              type="button"
              buttonSize="middle"
              value={statusFilter}
              onChange={(event) => changeStatusFilter(event.target.value as StatusFilterType)}
              options={[
                {
                  label: isMobile ? (
                    <span className="flex items-center gap-1.5">
                      <span>{t("全部")}</span>
                      <span className="text-xs text-fg-muted tabular-nums">{globalFilterCounts.all}</span>
                    </span>
                  ) : (
                    t("全部 ({{value0}})", { value0: globalFilterCounts.all })
                  ),
                  value: "all",
                },
                {
                  label: isMobile ? (
                    <span className="flex items-center gap-1.5">
                      {globalFilterCounts.active > 0 && <span className="size-1.5 rounded-full bg-brand-500 animate-pulse" />}
                      <span>{t("下载中")}</span>
                      <span className="text-xs text-fg-muted tabular-nums">{globalFilterCounts.active}</span>
                    </span>
                  ) : (
                    <span className="flex items-center gap-1.5">
                      {globalFilterCounts.active > 0 && <span className="size-1.5 rounded-full bg-brand-500 animate-pulse" />}
                      <span>{t("下载中 (")}{globalFilterCounts.active})</span>
                    </span>
                  ),
                  value: "active",
                },
                {
                  label: isMobile ? (
                    <span className="flex items-center gap-1.5">
                      <span>{t("已完成")}</span>
                      <span className="text-xs text-fg-muted tabular-nums">{globalFilterCounts.completed}</span>
                    </span>
                  ) : (
                    t("已完成 ({{value0}})", { value0: globalFilterCounts.completed })
                  ),
                  value: "completed",
                },
                {
                  label: isMobile ? (
                    <span className="flex items-center gap-1.5">
                      {globalFilterCounts.failed > 0 && <span className="size-1.5 rounded-full bg-danger" />}
                      <span>{t("失败")}</span>
                      <span className="text-xs text-fg-muted tabular-nums">{globalFilterCounts.failed}</span>
                    </span>
                  ) : (
                    <span className="flex items-center gap-1.5">
                      {globalFilterCounts.failed > 0 && <span className="size-1.5 rounded-full bg-danger" />}
                      <span>{t("失败/异常 (")}{globalFilterCounts.failed})</span>
                    </span>
                  ),
                  value: "failed",
                },
              ]}
            />
          </div>
        </Tooltip>
          <span className="toolbar-summary">{serverFiltering ? t("全量筛选") : t("本页筛选")}</span>
        </div>

        <div className="toolbar-filters">
          <Input
            prefix={<Search size={16} />}
            aria-label={t("按标题、用户名、错误信息或 ID 搜索...")}
            placeholder={t("搜索本页任务、用户名或错误...")}
            value={searchInput}
            onChange={setSearchInput}
            showClear
            className="toolbar-search"
          />

          <Select
            value={kindFilter}
            onChange={(value) => changeKindFilter(String(value))}
            className="toolbar-select"
            aria-label={t("任务类型")}
            optionList={[
              { value: "all", label: t("全部任务类型") },
              ...jobKindFilterValues.map((kind) => ({ value: kind, label: kindLabel(kind) })),
            ]}
          />

          <Button
            type={pinFailedRetry ? "primary" : "tertiary"}
            theme={pinFailedRetry ? "light" : "borderless"}
            icon={<Pin size={15} />}
            aria-pressed={pinFailedRetry}
            onClick={togglePinFailedRetry}
          >
            {t("置顶失败重试")}
          </Button>
        </div>
      </div>

      {hasActiveFilter && (
        <div className="active-filter-tags">
          {statusFilter !== "all" && (
            <Button
              size="small"
              type="tertiary"
              className="filter-chip"
              icon={<X size={12} />}
              iconPosition="right"
              aria-label={`${t("移除筛选")} ${statusFilter === "active" ? t("下载中") : statusFilter === "completed" ? t("已完成") : t("异常 / 失败")}`}
              onClick={() => changeStatusFilter("all")}
            >
              {t("当前状态")}: {statusFilter === "active" ? t("下载中") : statusFilter === "completed" ? t("已完成") : t("异常 / 失败")}
            </Button>
          )}
          {kindFilter !== "all" && (
            <Button
              size="small"
              type="tertiary"
              className="filter-chip"
              icon={<X size={12} />}
              iconPosition="right"
              aria-label={`${t("移除筛选")} ${kindLabel(kindFilter as Job["kind"])}`}
              onClick={() => changeKindFilter("all")}
            >
              {t("类型")}: {kindLabel(kindFilter as Job["kind"])}
            </Button>
          )}
          {filterKeyword.trim() !== "" && (
            <Button
              size="small"
              type="tertiary"
              className="filter-chip"
              icon={<X size={12} />}
              iconPosition="right"
              aria-label={`${t("移除筛选")} ${filterKeyword.trim()}`}
              onClick={() => changeSearchKeyword("")}
            >
              {filterKeyword.trim()}
            </Button>
          )}
          {!(isMobile && filteredJobs.length === 0) && (
            <Button size="small" theme="borderless" type="tertiary" onClick={clearFilters}>
              {t("清除筛选")}
            </Button>
          )}
        </div>
      )}

      {pinFailedRetry && failedTweetCount > 0 && (
        <Alert
          type="warning"
          showIcon
          message={t("当前有 {{value0}} 条失败推文待重试", { value0: failedTweetCount })}
          action={
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="small"
                type="tertiary"
                loading={retryAllFailed.isPending}
                disabled={retryAllFailed.isPending}
                onClick={() => retryAllFailed.mutate()}
              >
                {t("重试失败推文")}
              </Button>
              <Button size="small" type="tertiary" onClick={onOpenFailedDrawer}>
                {t("查看失败队列")}
              </Button>
            </div>
          }
        />
      )}

      {selectedJobs.length > 0 && (
        <div className="task-bulk-toolbar" role="toolbar" aria-label={t("批量操作")}>
          <span className="text-xs text-fg-muted">{t("已选择 {{value0}} 个任务", { value0: selectedJobs.length })}</span>
          {cancelableSelectedIds.length > 0 && (
            <Button
              type="danger"
              size="small"
              loading={bulkCancel.isPending}
              disabled={tableLoading || bulkCancel.isPending || bulkRetry.isPending || bulkDelete.isPending}
              onClick={() => handleBulkCancel(cancelableSelectedIds)}
            >
              {t("批量取消")}
            </Button>
          )}
          {retryableSelectedIds.length > 0 && (
            <Button
              size="small"
              type="tertiary"
              loading={bulkRetry.isPending}
              disabled={tableLoading || bulkCancel.isPending || bulkRetry.isPending || bulkDelete.isPending}
              onClick={() => handleBulkRetry(retryableSelectedIds)}
            >
              {t("批量重试")}
            </Button>
          )}
          {deletableSelectedIds.length > 0 && (
            <Popconfirm
              title={t("批量删除")}
              description={t("确认删除选中的 {{value0}} 个已结束任务？", { value0: deletableSelectedIds.length })}
              okText={t("删除")}
              cancelText={t("取消")}
              disabled={tableLoading || bulkCancel.isPending || bulkRetry.isPending || bulkDelete.isPending}
              onConfirm={() => handleBulkDelete(deletableSelectedIds)}
            >
              <Button
                size="small"
                type="danger"
                loading={bulkDelete.isPending}
                disabled={tableLoading || bulkCancel.isPending || bulkRetry.isPending || bulkDelete.isPending}
              >
                {t("批量删除")}
              </Button>
            </Popconfirm>
          )}
          <Button size="small" type="tertiary" disabled={tableLoading} onClick={() => setSelectedJobIds(new Set())}>{t("清除选择")}</Button>
        </div>
      )}

      {/* 任务核心数据表格 */}
      {isMobile ? (
        <div className="min-w-0 space-y-4">
          {filteredJobs.length > 0 && (
            <div className="flex items-center justify-between px-1 text-xs text-fg-muted">
              <Checkbox
                aria-label={t("选择当前页任务")}
                checked={allVisibleSelected}
                indeterminate={selectedJobs.length > 0 && !allVisibleSelected}
                disabled={tableLoading || filteredJobs.length === 0}
                onChange={event => toggleAllVisible(event.target.checked === true)}
              >
                {t("全选本页")}
              </Checkbox>
              <span>{summaryLabel}</span>
            </div>
          )}
          <LoadingOverlay loading={tableLoading}>
            {filteredJobs.length === 0 ? (
              <Empty description={hasActiveFilter ? t(serverFiltering ? "没有匹配的任务" : "本页没有匹配的任务") : t("暂无任务")}>
                {hasActiveFilter && <Button onClick={clearFilters}>{t("清除筛选")}</Button>}
              </Empty>
            ) : (
              <ul aria-label={t("任务列表")} className="mobile-job-list">
                {filteredJobs.map((job) => (
                  <MobileJobCard
                    key={job.id}
                    job={job}
                    pinned={pinFailedRetry && job.kind === "failed_retry"}
                    selected={selectedJobIds.has(job.id)}
                    selectionDisabled={tableLoading}
                    onSelect={toggleSelected}
                    renderActions={renderJobActions}
                    onOpenDetails={openJobDetails}
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
            rowKey={jobRowKey}
            loading={tableLoading}
            className="task-table data-table"
            emptyText={hasActiveFilter ? t(serverFiltering ? "没有匹配的任务" : "本页没有匹配的任务") : t("暂无任务")}
            footer={paginationFooter}
          />
      )}
      <Drawer open={Boolean(detailJob)} onClose={closeJobDetails} width="min(100vw, 860px)" title={detailJob?.title || t("任务详情")} className="task-detail-drawer">
        {detailJob && <>
          <div className="task-detail-summary">
            <JobProgress job={detailJob} />
          </div>
          {retryCreatedJobId && <Alert type="success" message={t("重试任务已创建")} action={<a href={`/tasks?q=${retryCreatedJobId}`} className="text-brand-600">{t("查看新任务")}</a>} />}
          <ExpandedJobDetails key={detailJob.id} job={detailJob} onRetry={() => retry.mutate(detailJob.id)} retryingJobId={retry.isPending ? retry.variables : undefined} />
        </>}
      </Drawer>
    </div>
  );
});

// 每个单元格自己 useTranslation()：语言切换会通过 context 触发重渲染，
// React.memo 只拦住"任务对象没变"的普通列表刷新。
const JobTitleCell = React.memo(function JobTitleCell({ job, pinned = false, onOpenDetails }: { job: Job; pinned?: boolean; onOpenDetails: (id: number) => void }) {
  useTranslation();
  return (
    <div className="flex min-w-0 items-center gap-2.5 py-0.5">
      <Avatar color={jobKindAvatarColor(job.kind)}>
        <JobKindIcon kind={job.kind} className="size-4" />
      </Avatar>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          {pinned && <Tag tone="warning" size="sm">{t("已置顶")}</Tag>}
          <button type="button" className="task-title-link" onClick={() => onOpenDetails(job.id)} title={job.title || kindLabel(job.kind)}>
            {job.title || kindLabel(job.kind)}
          </button>
        </div>
        <div className="truncate text-xs text-fg-muted" title={job.input}>
          {t("目标: ")}{job.input} · #JOB-{job.id}
        </div>
      </div>
    </div>
  );
});

const JobKindCell = React.memo(function JobKindCell({ job }: { job: Job }) {
  useTranslation();
  return (
    <Tag tone="default" icon={<JobKindIcon kind={job.kind} />}>
      {kindLabel(job.kind)}
    </Tag>
  );
});

const JobUpdatedCell = React.memo(function JobUpdatedCell({ updatedAt }: { updatedAt: string }) {
  return (
    <span className="text-xs text-fg-subtle tabular-nums">
      {formatDateTime(updatedAt)}
    </span>
  );
});

const JobProgress = React.memo(function JobProgress({ job }: { job: Job }) {
  useTranslation();
  const percent = clampPercent(job.progress);
  const meta = getJobStatusPresentation(job.status);
  const { displayMsg, tooltip, hasError } = getJobProgressDisplay(job);
  return (
    <Tooltip content={
      <div className="task-progress-tooltip">
        <div className="flex items-center justify-between gap-4 font-medium">
          <span>{meta.label}</span><span className="tabular-nums">{percent}%</span>
        </div>
        <div className={cn("mt-2", hasError && "font-medium")}>{displayMsg}</div>
        {tooltip !== displayMsg && <div className="mt-2 whitespace-pre-wrap">{tooltip}</div>}
      </div>
    }>
      <div className="task-progress" aria-label={`${meta.label} · ${percent}%`}>
        <div className="flex items-center justify-between gap-2"><Tag tone={meta.tone} icon={meta.icon}>{meta.label}</Tag>{job.status === "downloading" && <span className="text-xs tabular-nums">{percent}%</span>}</div>
        {job.status === "downloading" && <Progress percent={percent} showInfo={false} aria-label={t("下载进度")} />}
        <span className={cn("task-progress-message", hasError && "text-danger")}>{displayMsg}</span>
      </div>
    </Tooltip>
  );
});

const MobileJobCard = React.memo(function MobileJobCard({
  job,
  pinned = false,
  selected,
  selectionDisabled,
  onSelect,
  renderActions,
  onOpenDetails,
}: {
  job: Job;
  pinned?: boolean;
  selected: boolean;
  selectionDisabled: boolean;
  onSelect: (id: number, checked: boolean) => void;
  renderActions: (job: Job, includeDetails?: boolean) => React.ReactNode;
  onOpenDetails: (id: number) => void;
}) {
  useTranslation();
  return (
    <li className="mobile-job-card min-w-0 overflow-hidden rounded-card border border-line bg-surface">
      <div className="space-y-3 p-3">
        <div className="mobile-job-heading">
          <Checkbox
            aria-label={t("选择任务 #{{value0}}", { value0: job.id })}
            checked={selected}
            disabled={selectionDisabled}
            onChange={event => onSelect(job.id, event.target.checked === true)}
          />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-1.5">
              {pinned && <Tag tone="warning" size="sm">{t("已置顶")}</Tag>}
              <h2 className="break-words text-sm font-semibold text-fg">{job.title || kindLabel(job.kind)}</h2>
            </div>
            <p className="mt-1 break-all text-xs text-fg-muted">{job.input}</p>
          </div>
          <JobProgress job={job} />
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-fg-subtle">
          <Tag icon={<JobKindIcon kind={job.kind} />}>{kindLabel(job.kind)}</Tag>
          <span className="tabular-nums">#JOB-{job.id}</span>
          <time className="ml-auto text-xs tabular-nums" dateTime={job.updatedAt}>{formatDateTime(job.updatedAt)}</time>
        </div>
        <div className="flex items-center justify-between border-t border-line pt-2">
          <Button
            theme="borderless"
            type="tertiary"
            aria-label={t("查看任务详情")}
            onClick={() => onOpenDetails(job.id)}
            icon={<FolderCheck className="size-4" />}
          >
            {t("查看详情")}
          </Button>
          {renderActions(job)}
        </div>
      </div>
    </li>
  );
});

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
      className="task-error-alert border-danger/30"
      message={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span>{title}</span>
          <Button
            theme="borderless"
            type="danger"
            onClick={() => void handleCopy()}
            icon={copied ? <Check className="size-4 text-success" /> : <Copy className="size-4" />}
          >
            {copied ? t("已复制诊断信息") : t("复制错误信息")}
          </Button>
        </div>
      }
      description={
        <div className="mt-2 space-y-2.5">
          {groups.length === 0 ? (
            <div className="font-mono text-xs text-danger-strong font-medium break-all">
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
                    className="task-error-group space-y-2"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <AlertCircle className="size-3.5 text-danger shrink-0" />
                        <span className="text-xs font-semibold text-danger-strong break-all">
                          {group.reason}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 shrink-0">
                        {group.action && (
                          <Tag size="sm" tone="neutral">
                            {group.action}
                          </Tag>
                        )}
                        <Tag size="sm" tone="danger" className="font-semibold">
                          {group.count} {group.countUnit ? t("种异常") : t("个失败")}
                        </Tag>
                      </div>
                    </div>

                    {group.targets.length > 0 && (
                      <div className="space-y-1.5 pt-1.5 border-t border-line/50">
                        <div className="flex items-center justify-between text-xs text-fg-muted">
                          <span>{t("受影响账号 (")}{group.targets.length}):</span>
                          {hasMoreTargets && (
                            <button
                              type="button"
                              aria-expanded={isExpanded}
                              onClick={() => toggleExpand(group.id)}
                              className="text-xs text-brand-600 hover:underline cursor-pointer"
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
                            <Tag size="sm" tone="warning" className="tabular-nums">
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
              <span className="shrink-0 font-semibold text-fg-subtle">{t("状态概况:")}</span>
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

  const [activeTab, setActiveTab] = useState<string>(hasJobError ? "overview" : "files");
  const hasManualTabSelectionRef = useRef(false);

  const handleTabChange = useCallback((tab: string) => {
    hasManualTabSelectionRef.current = true;
    setActiveTab(tab);
  }, []);

  React.useEffect(() => {
    if (hasManualTabSelectionRef.current) return;
    if (downloads.length === 0 && failed.length > 0) {
      setActiveTab("errors");
    } else if (downloads.length > 0 && failed.length === 0 && !hasJobError) {
      setActiveTab("files");
    }
  }, [downloads.length, failed.length, hasJobError]);

  const renderDownloadsList = () => (
    <PaginatedList<DownloadRecord>
      items={downloads}
      bordered={false}
      pageSize={6}
      itemName={t("个文件")}
      emptyDescription={t("暂无下载文件记录")}
      renderItem={(item) => {
        const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
        const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
        return (
          <div className="flex items-center justify-between gap-3 text-xs">
            <div className="flex min-w-0 flex-1 items-start gap-2.5">
              <span className="flex size-7 shrink-0 items-center justify-center rounded-card bg-brand-500/10 text-xs font-semibold text-brand-600">
                {ext}
              </span>
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex items-center gap-2">
                  <span className="truncate font-semibold text-fg" title={fileName}>
                    {fileName}
                  </span>
                  {item.bytes > 0 && (
                    <span className="shrink-0 text-xs text-fg-subtle tabular-nums">
                      {formatBytes(item.bytes)}
                    </span>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-1.5 font-mono text-xs text-fg-muted">
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
                  icon={<Copy className="size-4" />}
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
      bordered={false}
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
              <div className="font-semibold text-danger-strong break-all">
                {item.error || t("媒体下载失败")}
              </div>
              <div className="flex flex-wrap items-center gap-1.5 font-mono text-xs text-fg-muted">
                <span
                  className="max-w-full truncate sm:max-w-md md:max-w-lg lg:max-w-xl text-fg-subtle"
                  title={item.mediaUrl}
                >
                  {item.mediaUrl}
                </span>
                {item.createdAt && (
                  <span className="text-fg-subtle/70 tabular-nums">
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
                icon={<Copy className="size-4" />}
                onClick={() => void copyToClipboard(item.mediaUrl, t("媒体地址"))}
                aria-label={t("复制媒体地址")}
              />
            </Tooltip>
          </div>
        </div>
      )}
    />
  );

  const renderFilesContent = () => filesQuery.isLoading ? (
    <div className="py-6 text-center">
      <Spinner className="size-5 mx-auto" />
      <p className="mt-2 text-xs text-fg-muted">{t("正在获取已下载媒体文件清单...")}</p>
    </div>
  ) : filesQuery.isError ? (
    <Alert
      type="error"
      message={t("读取文件清单失败")}
      description={getErrorMessage(filesQuery.error)}
      action={<Button type="tertiary" onClick={() => void filesQuery.refetch()}>{t("重试")}</Button>}
    />
  ) : downloads.length === 0 ? (
    hasJobError ? <Empty description={t("任务未生成已下载文件记录")} />
      : job.status === "pending" || job.status === "resolving" || job.status === "downloading"
        ? <div className="py-6 text-center text-xs text-fg-muted">{t("任务正在执行中，等待媒体拉取入库...")}</div>
        : <Empty description={t("暂无已归档的文件记录")} />
  ) : renderDownloadsList();

  const renderErrorsContent = () => (
    <div className="space-y-3">
      {hasJobError && <JobErrorAlert job={job} />}
      {failed.length > 0 ? renderFailedList() : !hasJobError && <Empty description={t("暂无错误与日志")} />}
    </div>
  );

  return (
    <div className="task-details">
      <div className="task-details-heading">
        <dl className="task-details-meta">
          <div><dt>{t("任务 ID")}</dt><dd className="tabular-nums">#{job.id}</dd></div>
          <div><dt>{t("创建时间")}</dt><dd className="tabular-nums">{formatDateTime(job.createdAt)}</dd></div>
          <div><dt>{t("入库文件")}</dt><dd>
                <div className="flex flex-wrap items-center gap-2">
                  <Tag tone="success">
                    {filesQuery.data?.truncated ? t("已加载 ") : t("成功 ")}{downloads.length}
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
          </dd></div>
        </dl>
        <div className="task-details-actions">
        <Typography.Text link={{ href: `/tweets?jobId=${job.id}` }}>{t("archive.title")}</Typography.Text>
        {retryableStatuses.includes(job.status) && (
          <Button
            type="tertiary"
            icon={<RotateCcw className="size-4" />}
            onClick={onRetry}
            loading={retryingJobId === job.id}
            aria-busy={retryingJobId === job.id}
            disabled={retryingJobId !== undefined}
          >
            {t("重试此任务")}</Button>
        )}
        </div>
      </div>

      {filesQuery.data?.truncated && (
        <Alert type="warning" message={t("文件清单过长，每类仅展示最近 5000 条记录")} />
      )}
      <Tabs
        value={activeTab}
        onChange={handleTabChange}
        items={[
          { key: "overview", label: t("概览"), children: <div className="space-y-3"><p className="text-xs text-fg-muted">{job.message || t("暂无状态说明")}</p>{hasJobError ? <JobErrorAlert job={job} /> : <Empty description={t("任务未报告错误")} />}</div> },
          { key: "files", label: <span className="flex items-center gap-1.5"><span>{t("文件列表")}</span><Tag tone="success" size="sm" className="tabular-nums">{downloads.length}</Tag></span>, children: renderFilesContent() },
          { key: "errors", label: <span className="flex items-center gap-1.5"><span className={failed.length || hasJobError ? "text-danger" : ""}>{t("错误与日志")}</span>{(failed.length > 0 || hasJobError) && <Tag tone="danger" size="sm" className="tabular-nums">{failed.length || 1}</Tag>}</span>, children: renderErrorsContent() },
        ]}
      />
    </div>
  );
}
