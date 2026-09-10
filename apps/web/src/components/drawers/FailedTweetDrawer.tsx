import { CloseCircleOutlined } from "@ant-design/icons";
import { Drawer, Grid, Space, Tag, Typography } from "antd";
import React from "react";
import type { FailedTweet } from "../../lib/api";
import { FailedTweetQueue } from "../jobs/FailedTweetQueue";

export interface FailedTweetDrawerProps {
  open: boolean;
  onClose: () => void;
  items: FailedTweet[];
  total: number;
}

export function FailedTweetDrawer({
  open,
  onClose,
  items,
  total,
}: FailedTweetDrawerProps) {
  const screens = Grid.useBreakpoint();

  return (
    <Drawer
      destroyOnHidden
      open={open}
      title={
        <Space size={8}>
          <CloseCircleOutlined style={{ color: "#ef4444" }} />
          <Typography.Text strong>失败推文队列</Typography.Text>
          <Tag color="error" style={{ margin: 0 }} className="font-mono">
            {total}
          </Tag>
        </Space>
      }
      size={screens.md ? 760 : "100%"}
      onClose={onClose}
    >
      <FailedTweetQueue items={items} total={total} />
    </Drawer>
  );
}
