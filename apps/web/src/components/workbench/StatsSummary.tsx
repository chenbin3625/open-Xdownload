import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  SyncOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Button, Card, Col, Flex, Row, Statistic, Typography } from "antd";
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
      hint: "历史全部下载任务",
    },
    {
      key: "active",
      label: "正在进行",
      value: stats.active,
      icon: <SyncOutlined spin={stats.active > 0} />,
      hint: stats.active > 0 ? "正在并发下载与解析中" : "暂无活跃任务",
    },
    {
      key: "completed",
      label: "已完成",
      value: stats.completed,
      icon: <CheckCircleOutlined />,
      hint: "所有媒体均已保存完毕",
    },
    {
      key: "failed",
      label: "异常 / 失败",
      value: stats.failed + failedTweetCount,
      icon: <CloseCircleOutlined />,
      hint: failedTweetCount > 0 ? `含 ${failedTweetCount} 条推文待重试` : "无待处理失败项",
      clickable: failedTweetCount > 0 && !!onOpenFailedDrawer,
      onClick: onOpenFailedDrawer,
    },
  ];

  return (
    <Card>
      <Row gutter={[24, 20]}>
        {cards.map((item) => (
          <Col xs={12} lg={6} key={item.key}>
            <Flex vertical gap={4}>
              <Statistic title={item.label} value={item.value} prefix={item.icon} />
              {item.clickable ? (
                <Button type="link" onClick={item.onClick}>
                  {item.hint}
                </Button>
              ) : (
                <Text type="secondary">{item.hint}</Text>
              )}
            </Flex>
          </Col>
        ))}
      </Row>
    </Card>
  );
}
