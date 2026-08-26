import React from "react";
import type { DashboardStats } from "../../lib/api";

const numberFormatter = new Intl.NumberFormat("zh-CN");

export const StatsSummary = React.memo(function StatsSummary({
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
      tone: "primary",
      hint: "历史全部下载任务",
    },
    {
      key: "active",
      label: "正在进行",
      value: stats.active,
      tone: "processing",
      hint: stats.active > 0 ? "正在并发下载与解析中" : "暂无活跃任务",
    },
    {
      key: "completed",
      label: "已完成",
      value: stats.completed,
      tone: "success",
      hint: "所有媒体均已保存完毕",
    },
    {
      key: "failed",
      label: "异常 / 失败",
      value: stats.failed + failedTweetCount,
      tone: "danger",
      hint: failedTweetCount > 0 ? `含 ${failedTweetCount} 条推文待重试` : "无待处理失败项",
      clickable: failedTweetCount > 0 && !!onOpenFailedDrawer,
      onClick: onOpenFailedDrawer,
    },
  ];

  return (
    <div className="stats-summary-grid">
      {cards.map((card) => (
        <div
          key={card.key}
          className={`stat-card stat-card-${card.tone} ${card.clickable ? "stat-card-clickable" : ""}`}
          onClick={card.onClick}
          role={card.clickable ? "button" : undefined}
          tabIndex={card.clickable ? 0 : undefined}
          onKeyDown={(event) => {
            if (card.clickable && (event.key === "Enter" || event.key === " ")) {
              event.preventDefault();
              card.onClick?.();
            }
          }}
        >
          <div className="stat-card-header">
            <span className="stat-card-label">{card.label}</span>
          </div>
          <div className="stat-card-value">{numberFormatter.format(card.value)}</div>
          <div className="stat-card-hint">{card.hint}</div>
        </div>
      ))}
    </div>
  );
});
