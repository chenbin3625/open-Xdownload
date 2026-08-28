import {
  ClockCircleOutlined,
  ExclamationCircleOutlined,
  FolderOpenOutlined,
  PictureOutlined,
  PlusOutlined,
  SettingOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Badge, Button, Progress, Space } from "antd";
import React from "react";
import type { SectionKey } from "../../lib/useRouteState";

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

interface NavItemConfig {
  key: SectionKey;
  label: string;
  icon: React.ReactNode;
  badge?: React.ReactNode;
}

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
  const currentKey =
    activeSection === "overview" ||
    activeSection === "workbench" ||
    activeSection === "tasks"
      ? "tasks"
      : activeSection;

  // 4 大模块导航项（不分组，直接平铺展示）
  const navItems: NavItemConfig[] = [
    {
      key: "tasks",
      label: "任务调度中心",
      icon: <UnorderedListOutlined className="text-base" />,
      badge:
        activeJobsCount > 0 ? (
          <Badge
            count={`${activeJobsCount} 运行`}
            style={{ backgroundColor: "#0ea5e9", color: "#ffffff" }}
          />
        ) : totalJobsCount > 0 ? (
          <span className="h-5 px-2 rounded-full text-[11px] font-mono font-medium inline-flex items-center justify-center bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400 border border-slate-200/80 dark:border-slate-700/60 shrink-0">
            {totalJobsCount}
          </span>
        ) : null,
    },
    {
      key: "schedules",
      label: "自动归档计划",
      icon: <ClockCircleOutlined className="text-base" />,
      badge:
        schedulesCount > 0 ? (
          <span className="h-5 px-2 rounded-full text-[11px] font-mono font-medium inline-flex items-center justify-center bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400 border border-slate-200/80 dark:border-slate-700/60 shrink-0">
            {schedulesCount} 个
          </span>
        ) : null,
    },
    {
      key: "gallery",
      label: "媒体归档库",
      icon: <PictureOutlined className="text-base" />,
      badge: (
        <span className="h-5 px-2 rounded-full text-[11px] font-medium inline-flex items-center justify-center bg-indigo-500/15 text-indigo-600 dark:text-indigo-400 border border-indigo-500/30 shrink-0">
          NEW
        </span>
      ),
    },
    {
      key: "settings",
      label: "系统与存储配置",
      icon: <SettingOutlined className="text-base" />,
      badge: (
        <Badge
          status="success"
          text={
            <span className="text-[11px] text-emerald-600 dark:text-emerald-400 font-medium">
              就绪
            </span>
          }
        />
      ),
    },
  ];

  return (
    <aside className="w-64 bg-white dark:bg-slate-900/95 border-r border-slate-200 dark:border-slate-800 flex flex-col justify-between shrink-0 select-none transition-colors duration-200">
      <div className="p-3.5 space-y-4">
        {/* Brand Header */}
        <div className="flex items-center gap-3 px-2 py-1">
          <div className="w-9 h-9 rounded-xl bg-gradient-to-tr from-sky-500 via-sky-600 to-indigo-600 flex items-center justify-center shadow-md shadow-sky-500/25 text-white font-bold text-base shrink-0">
            𝕏
          </div>
          <div className="min-w-0">
            <div className="font-bold text-slate-900 dark:text-slate-100 tracking-tight flex items-center gap-1.5 text-[14px] leading-none">
              <span>open-Xdownload</span>
              <span className="text-[10px] font-mono font-semibold uppercase px-1.5 py-0.5 rounded-full bg-sky-500/10 text-sky-600 dark:text-sky-400 border border-sky-500/20">
                v{__APP_VERSION__}
              </span>
            </div>
            <p className="text-[11px] text-slate-400 dark:text-slate-500 truncate mt-1">
              推文多媒体下载控制台
            </p>
          </div>
        </div>

        {/* Primary Action Button using Ant Design Button */}
        <Button
          type="primary"
          block
          size="large"
          icon={<PlusOutlined />}
          onClick={onOpenCreateModal}
          className="!h-10 !rounded-xl !text-[13px] !font-medium shadow-sm shadow-sky-500/25"
        >
          新建下载 / 归档
        </Button>

        {/* Flat Navigation List (无分组，统一平铺) */}
        <nav className="space-y-1.5 pt-1">
          {navItems.map((item) => {
            const isActive = currentKey === item.key;
            return (
              <button
                key={item.key}
                type="button"
                onClick={() => onSectionChange(item.key)}
                className={`group w-full h-10 flex items-center justify-between px-2.5 rounded-xl transition-all duration-150 cursor-pointer ${
                  isActive
                    ? "bg-sky-50 dark:bg-sky-500/15 text-sky-600 dark:text-sky-400 border border-sky-200 dark:border-sky-500/30 font-semibold shadow-xs"
                    : "text-slate-600 dark:text-slate-300 hover:bg-slate-100/80 dark:hover:bg-slate-800/60 hover:text-slate-900 dark:hover:text-slate-100 border border-transparent font-medium"
                }`}
              >
                <div className="flex items-center gap-2.5 min-w-0">
                  <span
                    className={`w-7 h-7 rounded-lg flex items-center justify-center text-sm transition-colors shrink-0 ${
                      isActive
                        ? "bg-sky-500 text-white shadow-xs"
                        : "bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400 group-hover:bg-slate-200 dark:group-hover:bg-slate-700/80 group-hover:text-slate-700 dark:group-hover:text-slate-200"
                    }`}
                  >
                    {item.icon}
                  </span>
                  <span className="text-[13px] tracking-tight truncate">
                    {item.label}
                  </span>
                </div>
                {item.badge}
              </button>
            );
          })}
        </nav>

        {/* Failed Tweets Alert Box using Ant Design components */}
        {failedTweetCount > 0 && (
          <div className="p-3 bg-red-50/90 dark:bg-red-950/30 border border-red-200 dark:border-red-800/40 rounded-xl space-y-2">
            <div className="flex items-center justify-between text-xs text-red-600 dark:text-red-300 font-semibold">
              <Space size={6}>
                <ExclamationCircleOutlined className="text-red-500" />
                <span>失败推文队列</span>
              </Space>
              <Badge
                count={failedTweetCount}
                color="#ef4444"
                overflowCount={999}
              />
            </div>
            <p className="text-[11px] text-slate-500 dark:text-slate-400 leading-tight">
              部分推文媒体存在限流或网络超时
            </p>
            <Button
              danger
              size="small"
              block
              onClick={onOpenFailedDrawer}
              className="!rounded-lg !text-xs !h-7"
            >
              查看并批量重试 &rarr;
            </Button>
          </div>
        )}
      </div>

      {/* Footer Storage Indicator using Ant Design Progress & Badge */}
      <div className="p-3.5 border-t border-slate-200 dark:border-slate-800 text-xs space-y-2 bg-slate-50/70 dark:bg-slate-950/40">
        <div className="flex items-center justify-between text-slate-500 dark:text-slate-400">
          <Space size={6}>
            <FolderOpenOutlined className="text-slate-400" />
            <span className="font-medium text-[12px]">
              存储 ({storageType.toUpperCase()})
            </span>
          </Space>
          <Badge
            status="success"
            text={
              <span className="text-[11px] text-emerald-600 dark:text-emerald-400 font-medium">
                正常
              </span>
            }
          />
        </div>
        <Progress
          percent={65}
          size="small"
          strokeColor={{ "0%": "#0ea5e9", "100%": "#6366f1" }}
          showInfo={false}
        />
        <div className="flex items-center justify-between text-[11px] text-slate-500 dark:text-slate-400 font-mono truncate">
          <span className="truncate" title={storagePath}>
            {storagePath || "/downloads"}
          </span>
          <span className="text-slate-400 shrink-0 ml-1">就绪</span>
        </div>
      </div>
    </aside>
  );
}
