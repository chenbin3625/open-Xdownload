import { AlertCircle, RotateCcw, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useState } from "react";
import {
  clearFailedTweets,
  deleteFailedTweet,
  getFailedTweets,
  retryFailedTweets,
  type Dashboard,
  type FailedTweet,
} from "../../lib/api";
import {
  AppEmpty,
  AppPagination,
  CopyButton,
  EllipsisText,
  ListSkeleton,
  LoadingSurface,
  Stack,
  Toolbar,
  failedTweetPageSizeOptions,
  formatDateTime,
  notifyError,
} from "../common/CommonUI";
import { Button } from "../ui/Button";
import { Popconfirm } from "../ui/Overlay";
import { Tag } from "../ui/Tag";
import { toast } from "../ui/Toast";
import { dashboardMetaQueryRoot, failedTweetQueryRoot, jobsQueryRoot } from "../../lib/api";

export function FailedTweetQueue({
  items,
  total,
}: {
  items: FailedTweet[];
  total: number;
}) {
  const queryClient = useQueryClient();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  const failedTweetsQuery = useQuery({
    queryKey: [...failedTweetQueryRoot, page, pageSize],
    queryFn: ({ signal }) => getFailedTweets({ page, pageSize, signal }),
  });

  const fallbackPagination: Dashboard["pagination"] = {
    page,
    pageSize,
    total,
    totalPages: total > 0 ? Math.ceil(total / pageSize) : 0,
  };

  const pageItems = failedTweetsQuery.data?.items ?? (page === 1 ? items.slice(0, pageSize) : []);
  const pagination = failedTweetsQuery.data?.pagination ?? fallbackPagination;

  const refreshFailedTweets = () => {
    queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot });
    queryClient.invalidateQueries({ queryKey: dashboardMetaQueryRoot });
    queryClient.invalidateQueries({ queryKey: jobsQueryRoot });
  };

  const retryAll = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (job) => {
      refreshFailedTweets();
      toast.success({
        message: "失败推文已加入重试",
        description: job.title || "已创建重试任务",
      });
    },
    onError: notifyError("重试失败"),
  });

  const removeOne = useMutation({
    mutationFn: deleteFailedTweet,
    onSuccess: () => {
      if (page > 1 && pageItems.length <= 1) {
        setPage(page - 1);
      }
      refreshFailedTweets();
      toast.success({ message: "失败记录已删除" });
    },
    onError: notifyError("删除失败"),
  });

  const clearAll = useMutation({
    mutationFn: clearFailedTweets,
    onSuccess: () => {
      setPage(1);
      refreshFailedTweets();
      toast.success({ message: "失败队列已清空" });
    },
    onError: notifyError("清空失败"),
  });

  return (
    <Stack size={12}>
      <Toolbar>
        <span className="text-xs text-fg-muted">
          {pagination.total > 0 ? `共 ${pagination.total} 条失败记录` : "暂无失败记录"}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="secondary"
            icon={<RotateCcw className="size-3.5" />}
            loading={retryAll.isPending}
            disabled={pagination.total === 0}
            onClick={() => retryAll.mutate()}
          >
            全部重试
          </Button>
          <Popconfirm
            title="清空失败队列"
            description="确认删除全部失败推文记录？"
            okText="清空"
            cancelText="取消"
            disabled={pagination.total === 0}
            onConfirm={() => clearAll.mutate()}
          >
            <Button
              size="sm"
              variant="danger"
              icon={<Trash2 className="size-3.5" />}
              loading={clearAll.isPending}
              disabled={pagination.total === 0}
            >
              清空
            </Button>
          </Popconfirm>
        </div>
      </Toolbar>

      {failedTweetsQuery.isLoading && pageItems.length === 0 ? (
        <ListSkeleton rows={pageSize} />
      ) : pageItems.length === 0 ? (
        <AppEmpty description="暂无失败推文" />
      ) : (
        <LoadingSurface loading={failedTweetsQuery.isFetching}>
          <div className="divide-y divide-line rounded-card border border-line bg-surface">
            {pageItems.map((item) => (
              <div key={item.id} className="flex items-start justify-between gap-3 p-3 transition-colors hover:bg-surface-hover">
                <div className="flex min-w-0 flex-1 items-start gap-3">
                  <div className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-danger-soft text-danger">
                    <AlertCircle className="size-4" />
                  </div>
                  <div className="min-w-0 flex-1 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-fg truncate">
                        {item.jobTitle || item.tweetId}
                      </span>
                      <Tag tone="neutral" className="font-mono">
                        {item.userScreenName ? `@${item.userScreenName}` : item.userId || "未知用户"}
                      </Tag>
                    </div>
                    <EllipsisText type="danger" title={item.error} className="text-xs">
                      {item.error || "未知错误"}
                    </EllipsisText>
                    <div className="flex flex-wrap items-center gap-3 text-xs text-fg-subtle">
                      <span className="font-mono">推文 {item.tweetId}</span>
                      <span>{formatDateTime(item.updatedAt || item.createdAt)}</span>
                      {item.entityName && <span>{item.entityName}</span>}
                    </div>
                  </div>
                </div>

                <div className="flex shrink-0 items-center gap-1.5">
                  <CopyButton value={item.tweetId} label="复制推文 ID" />
                  <Popconfirm
                    title="删除失败记录"
                    description="确认删除这条失败记录？"
                    okText="删除"
                    cancelText="取消"
                    onConfirm={() => removeOne.mutate(item.id)}
                  >
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Trash2 className="size-3.5 text-danger" />}
                      loading={removeOne.isPending && removeOne.variables === item.id}
                      aria-label="删除此条记录"
                    />
                  </Popconfirm>
                </div>
              </div>
            ))}
          </div>
        </LoadingSurface>
      )}

      <AppPagination
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
    </Stack>
  );
}
