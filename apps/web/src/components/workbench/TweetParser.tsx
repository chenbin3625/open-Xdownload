import {
  DownloadOutlined,
  FileTextOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Descriptions,
  Flex,
  Input,
  List,
  Tag,
  Typography,
  notification,
} from "antd";
import React, { useRef, useState } from "react";
import { createJob, parseTweetLink, type TweetData } from "../../lib/api";
import {
  CopyButton,
  EllipsisText,
  ListSkeleton,
  PaginatedList,
  Stack,
  getErrorMessage,
  mediaTypeLabel,
} from "../common/CommonUI";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";

const { Paragraph } = Typography;

export function TweetParser() {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const [parsed, setParsed] = useState<TweetData | null>(null);
  const [parsedSourceUrl, setParsedSourceUrl] = useState("");
  const latestParseUrl = useRef("");
  const parsedHasMedia = parsed !== null && parsed.media.length > 0;

  const parseMutation = useMutation({
    mutationFn: (targetUrl: string) => parseTweetLink(targetUrl),
    onSuccess: (data, targetUrl) => {
      if (targetUrl !== latestParseUrl.current) {
        return;
      }
      setParsed(data);
      setParsedSourceUrl(targetUrl);
      notification.success({
        message: "解析完成",
        description: `发现 ${data.media.length} 个媒体`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "解析失败",
        description: getErrorMessage(error),
      });
    },
  });

  const jobMutation = useMutation({
    mutationFn: () => createJob("tweet_link", parsedSourceUrl, parsed?.id ? `Tweet ${parsed.id}` : "推文任务"),
    onSuccess: () => {
      void invalidateWorkbenchQueries(queryClient);
      notification.success({ message: "下载任务已创建" });
    },
    onError: (error) => {
      notification.error({
        message: "创建失败",
        description: getErrorMessage(error),
      });
    },
  });

  function handleParse(targetUrl = url) {
    const trimmed = targetUrl.trim();
    if (!trimmed) return;
    latestParseUrl.current = trimmed;
    setUrl(trimmed);
    setParsed(null);
    setParsedSourceUrl("");
    parseMutation.mutate(trimmed);
  }

  return (
    <Stack size={16}>
      <Input.Search
        size="large"
        value={url}
        placeholder="https://x.com/user/status/123"
        enterButton="解析"
        loading={parseMutation.isPending}
        onChange={(event) => {
          const nextUrl = event.target.value;
          latestParseUrl.current = nextUrl.trim();
          setUrl(nextUrl);
          setParsed(null);
          setParsedSourceUrl("");
        }}
        onSearch={handleParse}
      />

      {parseMutation.isPending && !parsed ? <ListSkeleton rows={2} /> : null}

      {parsed ? (
        <Stack size={10}>
          <Descriptions
            size="small"
            column={{ xs: 1, md: 3 }}
            items={[
              { key: "author", label: "作者", children: `@${parsed.author.screenName || "unknown"}` },
              { key: "tweet", label: "推文", children: parsed.id },
              {
                key: "url",
                label: "链接",
                children: <CopyButton value={parsed.url} label="复制链接" />,
              },
            ]}
          />
          <Paragraph
            style={{
              marginBottom: 0,
              lineHeight: 1.6,
              overflowWrap: "anywhere",
              whiteSpace: "pre-wrap",
            }}
          >
            {parsed.text || "无正文"}
          </Paragraph>
          <MediaList media={parsed.media} />
          <Flex justify="flex-end">
            <Button
              type="primary"
              icon={<DownloadOutlined />}
              loading={jobMutation.isPending}
              disabled={!parsedHasMedia || !parsedSourceUrl}
              onClick={() => jobMutation.mutate()}
            >
              下载媒体
            </Button>
          </Flex>
        </Stack>
      ) : null}
    </Stack>
  );
}

export function MediaList({ media }: { media: TweetData["media"] }) {
  return (
    <PaginatedList
      size="small"
      bordered
      emptyDescription="未发现可下载媒体"
      itemName="个媒体"
      items={media}
      pageSize={5}
      renderItem={(item) => {
        const mediaUrl = item.bestUrl || item.url;
        return (
          <List.Item actions={[<CopyButton key="copy" value={mediaUrl} label="复制媒体地址" />]}>
            <List.Item.Meta
              avatar={<FileTextOutlined />}
              title={<Tag>{mediaTypeLabel(item.type)}</Tag>}
              description={
                <EllipsisText code title={mediaUrl}>
                  {mediaUrl}
                </EllipsisText>
              }
            />
          </List.Item>
        );
      }}
    />
  );
}
