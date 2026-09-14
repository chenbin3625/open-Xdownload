import {
  Clock,
  FolderOpen,
  Images,
  Plus,
  Settings,
  TriangleAlert,
  ListChecks,
} from "lucide-react";
import React from "react";
import { cn } from "../../lib/cn";
import type { SectionKey } from "../../lib/useRouteState";
import { Button } from "../ui/Button";
import { Divider } from "../ui/Feedback";
import { Tooltip } from "../ui/Overlay";
import { CountBadge, Tag } from "../ui/Tag";

export interface AppSidebarProps {
  activeSection: SectionKey;
  onSectionChange: (section: SectionKey) => void;
  onOpenCreateModal: () => void;
  onOpenFailedDrawer: () => void;
  totalJobsCount: number;
  activeJobsCount: number;
  schedulesCount: number;
  failedTweetCount: number;
  storageType?: string;
  storagePath?: string;
}

// overview / workbench / tasks 三个 section 都落到「任务调度中心」这一项上。
const taskCenterSections: SectionKey[] = ["overview", "workbench", "tasks"];

export function AppSidebar({
  activeSection,
  onSectionChange,
  onOpenCreateModal,
  onOpenFailedDrawer,
  totalJobsCount,
  activeJobsCount,
  schedulesCount,
  failedTweetCount,
  storageType = "local",
  storagePath = "/downloads",
}: AppSidebarProps) {
  const currentKey = taskCenterSections.includes(activeSection) ? "tasks" : activeSection;

  const navItems: {
    key: SectionKey;
    icon: React.ReactNode;
    label: string;
    badge?: React.ReactNode;
  }[] = [
    {
      key: "tasks",
      icon: <ListChecks className="size-4 shrink-0" />,
      label: "任务调度中心",
      badge:
        activeJobsCount > 0 ? (
          <Tag tone="brand" className="font-mono">{`${activeJobsCount} 运行`}</Tag>
        ) : totalJobsCount > 0 ? (
          <Tag className="font-mono">{totalJobsCount}</Tag>
        ) : null,
    },
    {
      key: "schedules",
      icon: <Clock className="size-4 shrink-0" />,
      label: "自动归档计划",
      badge: schedulesCount > 0 ? <Tag className="font-mono">{schedulesCount}</Tag> : null,
    },
    {
      key: "gallery",
      icon: <Images className="size-4 shrink-0" />,
      label: "媒体归档库",
    },
    {
      key: "settings",
      icon: <Settings className="size-4 shrink-0" />,
      label: "系统与存储配置",
    },
  ];

  return (
    <aside className="flex w-64 shrink-0 flex-col justify-between border-r border-line bg-surface select-none">
      <div className="p-3">
        {/* 品牌区 */}
        <div className="flex items-center gap-2.5 px-2 pt-1.5 pb-3">
          <span className="flex size-8.5 shrink-0 items-center justify-center rounded-[0.625rem] bg-brand-500 text-base font-bold text-white">
            𝕏
          </span>
          <div className="min-w-0">
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-semibold text-fg">open-Xdownload</span>
              <Tag tone="brand" className="font-mono text-[10px]">
                v{__APP_VERSION__}
              </Tag>
            </div>
            <p className="text-[11px] text-fg-muted">推文多媒体下载控制台</p>
          </div>
        </div>

        <Button variant="primary" block icon={<Plus className="size-4" />} onClick={onOpenCreateModal}>
          新建下载 / 归档
        </Button>

        {/* 手写导航列表：单选语义用 aria-current 表达，不需要 Menu 的全部键盘模型 */}
        <nav aria-label="主导航" className="mt-3 flex flex-col gap-0.5">
          {navItems.map((item) => {
            const active = item.key === currentKey;
            return (
              <button
                key={item.key}
                type="button"
                aria-current={active ? "page" : undefined}
                onClick={() => onSectionChange(item.key)}
                className={cn(
                  "flex h-10 cursor-pointer items-center gap-2.5 rounded-control px-2.5",
                  "text-sm transition-colors",
                  active
                    ? "bg-brand-50 font-medium text-brand-700"
                    : "text-fg-body hover:bg-surface-hover hover:text-fg",
                )}
              >
                <span className={active ? "text-brand-600" : "text-fg-subtle"}>{item.icon}</span>
                <span className="min-w-0 flex-1 truncate text-left">{item.label}</span>
                {item.badge}
              </button>
            );
          })}
        </nav>

        {/* 失败推文入口 */}
        {failedTweetCount > 0 && (
          <>
            <Divider />
            <div className="flex flex-col gap-2">
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-xs text-fg-body">
                  <TriangleAlert className="size-3.5 text-danger" />
                  失败推文队列
                </span>
                <CountBadge count={failedTweetCount} overflowCount={999} />
              </div>
              <Button variant="danger" size="sm" block onClick={onOpenFailedDrawer}>
                查看并批量重试
              </Button>
            </div>
          </>
        )}
      </div>

      {/* 底部存储状态 */}
      <div className="border-t border-line bg-surface-muted p-3">
        <div className="mb-1.5 flex items-center gap-1.5">
          <FolderOpen className="size-3.5 text-fg-subtle" />
          <span className="text-xs text-fg-muted">存储 ({storageType.toUpperCase()})</span>
        </div>
        <Tooltip title={storagePath}>
          <p className="truncate font-mono text-[11px] text-fg-muted">
            {storagePath || "/downloads"}
          </p>
        </Tooltip>
      </div>
    </aside>
  );
}
