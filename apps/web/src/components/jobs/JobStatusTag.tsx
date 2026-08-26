import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  DownloadOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
  StopOutlined,
} from "@ant-design/icons";
import { Tag } from "antd";
import React from "react";
import type { Job } from "../../lib/api";

export function JobStatusTag({ status }: { status: Job["status"] }) {
  const config = {
    pending: { color: "default", icon: <PauseCircleOutlined />, label: "排队" },
    resolving: { color: "processing", icon: <LoadingOutlined spin />, label: "解析" },
    downloading: { color: "blue", icon: <DownloadOutlined />, label: "下载" },
    completed: { color: "success", icon: <CheckCircleOutlined />, label: "完成" },
    completed_with_errors: { color: "warning", icon: <CloseCircleOutlined />, label: "部分失败" },
    failed: { color: "error", icon: <CloseCircleOutlined />, label: "失败" },
    canceled: { color: "default", icon: <StopOutlined />, label: "取消" },
  }[status];
  return (
    <Tag color={config.color} icon={config.icon}>
      {config.label}
    </Tag>
  );
}
