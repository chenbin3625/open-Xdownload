import { CloseCircleOutlined } from "@ant-design/icons";
import { Drawer, Grid, Space } from "antd";
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
        <Space>
          <CloseCircleOutlined className="text-red-500" />
          <span className="font-semibold text-slate-800 dark:text-slate-200">
            失败推文队列
          </span>
          <span className="text-xs px-2 py-0.5 rounded-full bg-red-100 dark:bg-red-500/20 text-red-700 dark:text-red-400 font-mono">
            {total}
          </span>
        </Space>
      }
      size={screens.md ? 760 : "100%"}
      onClose={onClose}
    >
      <FailedTweetQueue items={items} total={total} />
    </Drawer>
  );
}
