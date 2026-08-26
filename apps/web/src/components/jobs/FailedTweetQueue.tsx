import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useState } from "react";
import {
  clearFailedTweets,
  dashboardMetaQueryRoot,
  deleteFailedTweet,
  failedTweetQueryRoot,
  getFailedTweets,
  retryFailedTweets,
  type DashboardMeta,
  type DashboardPagination,
  type FailedTweetPage,
} from "../../lib/api";
import { formatDateTime, getErrorMessage } from "../../lib/format";
import { failedTweetPageSizeOptions } from "../../lib/pagination";
import { toast } from "../../lib/toast";
import { prependJobsToCaches } from "../../lib/useDashboardEvents";
import { CopyTextButton, ShellPagination } from "../common/ShellUI";

export function FailedTweetQueue({
  total,
}: {
  total: number;
}) {
  const queryClient = useQueryClient();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const failedTweetsQuery = useQuery({
    queryKey: [...failedTweetQueryRoot, page, pageSize],
    queryFn: ({ signal }) => getFailedTweets({ page, pageSize, signal }),
    staleTime: 8_000,
  });
  const fallbackPagination: DashboardPagination = {
    page,
    pageSize,
    total,
    totalPages: total > 0 ? Math.ceil(total / pageSize) : 0,
  };
  const pageItems = failedTweetsQuery.data?.items ?? [];
  const pagination = failedTweetsQuery.data?.pagination ?? fallbackPagination;
  const refreshFailedTweets = () => {
    queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot });
    queryClient.invalidateQueries({ queryKey: dashboardMetaQueryRoot });
  };
  const retryAll = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (job) => {
      prependJobsToCaches(queryClient, [job]);
      refreshFailedTweets();
      toast("失败推文已加入重试", { description: job.title || "已创建重试任务" });
    },
    onError: (error) => {
      toast("重试失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const removeOne = useMutation({
    mutationFn: deleteFailedTweet,
    onMutate: async (id) => {
      await queryClient.cancelQueries({ queryKey: failedTweetQueryRoot });
      const pages = queryClient.getQueriesData<FailedTweetPage>({ queryKey: failedTweetQueryRoot });
      const meta = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
      queryClient.setQueriesData<FailedTweetPage>({ queryKey: failedTweetQueryRoot }, (current) => {
        if (!current) return current;
        const items = current.items.filter((item) => item.id !== id);
        if (items.length === current.items.length) return current;
        const total = Math.max(0, current.pagination.total - 1);
        return {
          ...current,
          items,
          pagination: {
            ...current.pagination,
            total,
            totalPages: total > 0 ? Math.ceil(total / current.pagination.pageSize) : 0,
          },
        };
      });
      queryClient.setQueryData<DashboardMeta>(dashboardMetaQueryRoot, (current) => {
        if (!current) return current;
        return { ...current, failedTweetCount: Math.max(0, current.failedTweetCount - 1) };
      });
      return { pages, meta };
    },
    onSuccess: () => {
      const remaining = queryClient.getQueryData<FailedTweetPage>([...failedTweetQueryRoot, page, pageSize]);
      if (page > 1 && (remaining?.items.length ?? 0) === 0) {
        setPage(page - 1);
      }
      toast("失败记录已删除");
    },
    onError: (error, _id, context) => {
      for (const [key, data] of context?.pages ?? []) {
        queryClient.setQueryData(key, data);
      }
      if (context?.meta) {
        queryClient.setQueryData(dashboardMetaQueryRoot, context.meta);
      }
      toast("删除失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const clearAll = useMutation({
    mutationFn: clearFailedTweets,
    onMutate: async () => {
      await queryClient.cancelQueries({ queryKey: failedTweetQueryRoot });
      const pages = queryClient.getQueriesData<FailedTweetPage>({ queryKey: failedTweetQueryRoot });
      const meta = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
      queryClient.setQueriesData<FailedTweetPage>({ queryKey: failedTweetQueryRoot }, (current) => {
        if (!current) return current;
        return {
          items: [],
          pagination: { ...current.pagination, page: 1, total: 0, totalPages: 0 },
        };
      });
      queryClient.setQueryData<DashboardMeta>(dashboardMetaQueryRoot, (current) => {
        if (!current) return current;
        return { ...current, failedTweetCount: 0 };
      });
      return { pages, meta, page };
    },
    onSuccess: () => {
      setPage(1);
      toast("失败队列已清空");
    },
    onError: (error, _id, context) => {
      for (const [key, data] of context?.pages ?? []) {
        queryClient.setQueryData(key, data);
      }
      if (context?.meta) {
        queryClient.setQueryData(dashboardMetaQueryRoot, context.meta);
      }
      if (context?.page) {
        setPage(context.page);
      }
      toast("清空失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const busy = retryAll.isPending || clearAll.isPending;

  return (
    <div className="failed-stack">
      <div className="failed-toolbar">
        <span>{pagination.total > 0 ? `共 ${pagination.total} 条失败记录` : "暂无失败记录"}</span>
        <div className="failed-actions">
          <button
            type="button"
            className="job-text-btn"
            disabled={pagination.total === 0 || busy}
            onClick={() => retryAll.mutate()}
          >
            {retryAll.isPending ? "重试中…" : "全部重试"}
          </button>
          <button
            type="button"
            className="job-text-btn is-danger"
            disabled={pagination.total === 0 || busy}
            onClick={() => {
              if (window.confirm("确认删除全部失败推文记录？")) {
                clearAll.mutate();
              }
            }}
          >
            {clearAll.isPending ? "清空中…" : "清空"}
          </button>
        </div>
      </div>

      {failedTweetsQuery.isLoading && pageItems.length === 0 ? (
        <div className="shell-skeleton-block shell-skeleton-block-tall" />
      ) : pageItems.length === 0 ? (
        <p className="job-empty">暂无失败推文</p>
      ) : (
        <ul className={failedTweetsQuery.isFetching ? "failed-list is-fetching" : "failed-list"}>
          {pageItems.map((item) => (
            <li key={item.id} className="failed-item">
              <div className="failed-copy">
                <div className="job-title-cell">
                  <strong>{item.jobTitle || item.tweetId}</strong>
                  <span className="job-kind-tag">
                    {item.userScreenName ? `@${item.userScreenName}` : item.userId || "未知用户"}
                  </span>
                </div>
                <p className="failed-error" title={item.error}>{item.error || "未知错误"}</p>
                <div className="schedule-meta">
                  <span>推文 {item.tweetId}</span>
                  <span>{formatDateTime(item.updatedAt || item.createdAt)}</span>
                  {item.entityName ? <span>{item.entityName}</span> : null}
                </div>
              </div>
              <div className="schedule-actions">
                <CopyTextButton label="复制推文 ID" value={item.tweetId} />
                <button
                  type="button"
                  className="job-text-btn is-danger"
                  disabled={removeOne.isPending && removeOne.variables === item.id}
                  onClick={() => {
                    if (window.confirm("确认删除这条失败记录？")) {
                      removeOne.mutate(item.id);
                    }
                  }}
                >
                  删除
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <ShellPagination
        current={pagination.page}
        itemName="条记录"
        pageSize={pagination.pageSize}
        pageSizeOptions={failedTweetPageSizeOptions}
        total={pagination.total}
        onChange={(nextPage, nextPageSize) => {
          setPage(nextPageSize === pageSize ? nextPage : 1);
          setPageSize(nextPageSize);
        }}
      />
    </div>
  );
}
