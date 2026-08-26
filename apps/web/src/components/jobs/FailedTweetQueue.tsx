import {
  CloseCircleOutlined,
  DeleteOutlined,
  RetweetOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  List,
  Popconfirm,
  Space,
  Tag,
  Typography,
  notification,
} from "antd";
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
  getErrorMessage,
} from "../common/CommonUI";
import { dashboardMetaQueryRoot, jobsQueryRoot } from "../../lib/api";

const { Text } = Typography;
const failedTweetQueryRoot = ["failed-tweets"] as const;

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
      notification.success({
        message: "失败推文已加入重试",
        description: job.title || "已创建重试任务",
      });
    },
    onError: (error) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(error),
      });
    },
  });
  const removeOne = useMutation({
    mutationFn: deleteFailedTweet,
    onSuccess: () => {
      if (page > 1 && pageItems.length <= 1) {
        setPage(page - 1);
      }
      refreshFailedTweets();
      notification.success({ message: "失败记录已删除" });
    },
    onError: (error) => {
      notification.error({
        message: "删除失败",
        description: getErrorMessage(error),
      });
    },
  });
  const clearAll = useMutation({
    mutationFn: clearFailedTweets,
    onSuccess: () => {
      setPage(1);
      refreshFailedTweets();
      notification.success({ message: "失败队列已清空" });
    },
    onError: (error) => {
      notification.error({
        message: "清空失败",
        description: getErrorMessage(error),
      });
    },
  });

  return (
    <Stack size={10}>
      <Toolbar>
        <Text type="secondary">{pagination.total > 0 ? `共 ${pagination.total} 条失败记录` : "暂无失败记录"}</Text>
        <Space size={8} wrap>
          <Button
            size="small"
            icon={<RetweetOutlined />}
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
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={clearAll.isPending}
              disabled={pagination.total === 0}
            >
              清空
            </Button>
          </Popconfirm>
        </Space>
      </Toolbar>

      {failedTweetsQuery.isLoading && pageItems.length === 0 ? (
        <ListSkeleton rows={pageSize} />
      ) : pageItems.length === 0 ? (
        <AppEmpty description="暂无失败推文" />
      ) : (
        <LoadingSurface loading={failedTweetsQuery.isFetching}>
          <List
            bordered
            dataSource={pageItems}
            locale={{ emptyText: <AppEmpty description="暂无失败推文" /> }}
            renderItem={(item) => (
              <List.Item
                actions={[
                  <CopyButton key="copy" value={item.tweetId} label="复制推文 ID" />,
                  <Popconfirm
                    key="delete"
                    title="删除失败记录"
                    description="确认删除这条失败记录？"
                    okText="删除"
                    cancelText="取消"
                    onConfirm={() => removeOne.mutate(item.id)}
                  >
                    <Button
                      size="small"
                      danger
                      type="text"
                      icon={<DeleteOutlined />}
                      loading={removeOne.isPending && removeOne.variables === item.id}
                    />
                  </Popconfirm>,
                ]}
              >
                <List.Item.Meta
                  avatar={<Text type="danger"><CloseCircleOutlined /></Text>}
                  title={
                    <Space size={8} wrap>
                      <Text strong>{item.jobTitle || item.tweetId}</Text>
                      <Tag>{item.userScreenName ? `@${item.userScreenName}` : item.userId || "未知用户"}</Tag>
                    </Space>
                  }
                  description={
                    <Stack size={4}>
                      <EllipsisText type="danger" title={item.error}>
                        {item.error || "未知错误"}
                      </EllipsisText>
                      <Space size={10} wrap>
                        <Text type="secondary">推文 {item.tweetId}</Text>
                        <Text type="secondary">{formatDateTime(item.updatedAt || item.createdAt)}</Text>
                        {item.entityName ? <Text type="secondary">{item.entityName}</Text> : null}
                      </Space>
                    </Stack>
                  }
                />
              </List.Item>
            )}
            size="small"
          />
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
