import { t, useTranslation } from "../../lib/i18n";
import {
  Clock,
  FolderOpen,
  Images,
  Settings,
  TriangleAlert,
  ListChecks,
  FileText,
} from "lucide-react";
import React, { useLayoutEffect, useRef } from "react";
import { Button, Divider, Nav, Tooltip } from "@douyinfe/semi-ui";
import { cn } from "../../lib/cn";
import type { SectionKey } from "../../lib/useRouteState";

export interface AppSidebarProps {
  activeSection: SectionKey;
  onSectionChange: (section: SectionKey) => void;
  onOpenFailedDrawer: () => void;
  failedTweetCount: number;
  storageType?: string;
  storagePath?: string;
}

// overview / workbench / tasks 三个 section 都落到「任务中心」这一项上。
const taskCenterSections: SectionKey[] = ["overview", "workbench", "tasks"];

export function AppSidebar({
  activeSection,
  onSectionChange,
  onOpenFailedDrawer,
  failedTweetCount,
  storageType = "local",
  storagePath = "/downloads",
}: AppSidebarProps) {
  useTranslation();
  const currentKey = taskCenterSections.includes(activeSection) ? "tasks" : activeSection;
  const sidebar = useRef<HTMLElement>(null);

  const navItems: {
    key: SectionKey;
    icon: React.ReactNode;
    label: string;
  }[] = [
    {
      key: "tasks",
      icon: <ListChecks className="size-4 shrink-0" />,
      label: t("任务中心"),
    },
    {
      key: "schedules",
      icon: <Clock className="size-4 shrink-0" />,
      label: t("归档计划"),
    },
    {
      key: "gallery",
      icon: <Images className="size-4 shrink-0" />,
      label: t("媒体归档"),
    },
    { key: "tweets", icon: <FileText className="size-4 shrink-0" />, label: t("archive.title") },
    {
      key: "settings",
      icon: <Settings className="size-4 shrink-0" />,
      label: t("系统配置"),
    },
  ];

  useLayoutEffect(() => {
    sidebar.current?.querySelector('[role="menu"]')?.setAttribute("aria-label", t("主导航"));
    sidebar.current?.querySelectorAll('[role="menuitem"]').forEach((element, index) => {
      if (navItems[index]?.key === currentKey) element.setAttribute("aria-current", "page"); else element.removeAttribute("aria-current");
    });
  });

  return (
    <aside ref={sidebar} className="app-sidebar flex min-h-full w-full shrink-0 flex-col justify-between border-r border-line bg-surface select-none lg:w-56">
      <div className="p-3">
        {/* 品牌区 */}
        <div className="flex items-center gap-2.5 px-1 pt-1 pb-3">
          <img src="/icon.svg" alt="" className="size-8 shrink-0" />
          <div className="min-w-0">
            <div className="truncate text-sm font-bold tracking-tight text-fg">open-Xdownload</div>
            <p className="text-[11px] text-fg-muted font-medium truncate">{t("推文多媒体归档工作台")}</p>
          </div>
        </div>

        <Nav aria-label={t("主导航")} selectedKeys={[currentKey]} onSelect={data => onSectionChange(data.itemKey as SectionKey)} items={navItems.map(item => ({ itemKey: item.key, text: item.label, icon: item.icon }))} style={{ border: 0, width: "100%" }} />

        {/* 失败推文入口 */}
        {failedTweetCount > 0 && (
          <>
            <Divider className="my-3" />
            <div className="flex flex-col gap-2 rounded-control border border-danger/25 bg-danger-soft/50 p-2.5">
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-xs font-medium text-danger">
                  <TriangleAlert className="size-3.5 text-danger" />
                  {t("失败队列")}</span>
              </div>
              <Button type="danger" block onClick={onOpenFailedDrawer}>
                {t("处理失败")}</Button>
            </div>
          </>
        )}
      </div>

      {/* 底部存储状态 */}
      <div className="border-t border-line/80 bg-surface-muted/60 p-3">
        <div className="mb-1.5 flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <FolderOpen className="size-3.5 text-brand-500" />
            <span className="text-xs font-medium text-fg">{t("本地存储")}</span>
          </div>
          <span className="rounded bg-surface px-1.5 py-0.5 text-[10px] font-mono text-fg-muted border border-line">
            {storageType.toUpperCase()}
          </span>
        </div>
        <Tooltip content={storagePath}>
          <p className="truncate font-mono text-[11px] text-fg-muted hover:text-fg transition-colors">
            {storagePath || "/downloads"}
          </p>
        </Tooltip>
      </div>
    </aside>
  );
}
