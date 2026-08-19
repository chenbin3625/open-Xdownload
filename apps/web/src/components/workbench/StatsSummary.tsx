import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  SyncOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Col, Row, Typography } from "antd";
import React from "react";
import type { DashboardStats } from "../../lib/api";

const { Text } = Typography;

export function StatsSummary({
  stats,
  failedTweetCount = 0,
  onOpenFailedDrawer,
}: {
  stats: DashboardStats;
  failedTweetCount?: number;
  onOpenFailedDrawer?: () => void;
}) {
  const cards = [
    {
      key: "total",
      label: "任务总数",
      value: stats.total,
      icon: <UnorderedListOutlined />,
      tone: "primary",
      hint: "历史全部下载任务",
    },
    {
      key: "active",
      label: "正在进行",
      value: stats.active,
      icon: <SyncOutlined spin={stats.active > 0} />,
      tone: "processing",
      hint: stats.active > 0 ? "正在并发下载与解析中" : "暂无活跃任务",
    },
    {
      key: "completed",
      label: "已完成",
      value: stats.completed,
      icon: <CheckCircleOutlined />,
      tone: "success",
      hint: "所有媒体均已保存完毕",
    },
    {
      key: "failed",
      label: "异常 / 失败",
      value: stats.failed + failedTweetCount,
      icon: <CloseCircleOutlined />,
      tone: "danger",
      hint: failedTweetCount > 0 ? `含 ${failedTweetCount} 条推文待重试` : "无待处理失败项",
      clickable: failedTweetCount > 0 && !!onOpenFailedDrawer,
      onClick: onOpenFailedDrawer,
    },
  ];

  return (
    <Row gutter={[12, 12]} className="stats-summary-grid">
      {cards.map((card) => (
        <Col xs={12} sm={12} lg={6} key={card.key}>
          <div
            className={`stat-card stat-card-${card.tone} ${card.clickable ? "stat-card-clickable" : ""}`}
            onClick={card.onClick}
            role={card.clickable ? "button" : undefined}
            tabIndex={card.clickable ? 0 : undefined}
            onKeyDown={(e) => {
              if (card.clickable && (e.key === "Enter" || e.key === " ")) {
                e.preventDefault();
                card.onClick?.();
              }
            }}
          >
            <div className="stat-card-header">
              <span className="stat-card-icon">{card.icon}</span>
              <Text className="stat-card-label">{card.label}</Text>
            </div>
            <div className="stat-card-value">{card.value.toLocaleString()}</div>
            <div className="stat-card-hint">
              <Text type="secondary">{card.hint}</Text>
            </div>
          </div>
        </Col>
      ))}
    </Row>
  );
}
