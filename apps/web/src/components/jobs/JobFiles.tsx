import {
  CloseCircleOutlined,
  FileDoneOutlined,
} from "@ant-design/icons";
import { List, Space, Tabs, Typography } from "antd";
import type { TabsProps } from "antd";
import React from "react";
import { formatBytes, type DownloadRecord, type FailedMedia } from "../../lib/api";
import {
  CopyButton,
  EllipsisText,
  PaginatedList,
  iconStyles,
} from "../common/CommonUI";

const { Text } = Typography;

export function JobFiles({ downloads, failed }: { downloads: DownloadRecord[]; failed: FailedMedia[] }) {
  const total = downloads.length + failed.length;
  if (total === 0) {
    return (
      <PaginatedList
        emptyDescription="暂无文件记录"
        itemName="个文件"
        items={[] as DownloadRecord[]}
        pageSize={5}
        renderItem={() => null}
        size="small"
      />
    );
  }

  const items: TabsProps["items"] = [
    {
      key: "downloads",
      label: `已下载 ${downloads.length}`,
      children: (
        <PaginatedList
          emptyDescription="暂无下载文件"
          itemName="个文件"
          items={downloads}
          pageSize={5}
          renderItem={(item) => (
            <List.Item actions={[<CopyButton key="copy" value={item.filePath} label="复制文件路径" />]}>
              <List.Item.Meta
                avatar={<FileDoneOutlined style={iconStyles.success} />}
                title={
                  <EllipsisText title={item.filePath}>
                    {item.filePath}
                  </EllipsisText>
                }
                description={
                  <Space size={8} wrap>
                    <Text type="secondary">{formatBytes(item.bytes)}</Text>
                    <EllipsisText
                      type="secondary"
                      title={item.mediaUrl}
                      style={{ maxWidth: "min(760px, 58vw)" }}
                    >
                      {item.mediaUrl}
                    </EllipsisText>
                  </Space>
                }
              />
            </List.Item>
          )}
          size="small"
        />
      ),
    },
    {
      key: "failed",
      label: `失败 ${failed.length}`,
      children: (
        <PaginatedList
          emptyDescription="暂无失败媒体"
          itemName="个媒体"
          items={failed}
          pageSize={5}
          renderItem={(item) => (
            <List.Item actions={[<CopyButton key="copy" value={item.mediaUrl} label="复制媒体地址" />]}>
              <List.Item.Meta
                avatar={<CloseCircleOutlined style={iconStyles.danger} />}
                title={
                  <EllipsisText type="danger" title={item.error}>
                    {item.error}
                  </EllipsisText>
                }
                description={
                  <EllipsisText
                    type="secondary"
                    title={item.mediaUrl}
                    style={{ maxWidth: "min(760px, 58vw)" }}
                  >
                    {item.mediaUrl}
                  </EllipsisText>
                }
              />
            </List.Item>
          )}
          size="small"
        />
      ),
    },
  ];

  return <Tabs size="small" items={items} />;
}
