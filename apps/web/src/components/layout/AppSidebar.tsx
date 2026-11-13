import { t, useTranslation } from "../../lib/i18n";
import {
  Clock,
  Images,
  Settings,
  ListChecks,
  FileText,
} from "lucide-react";
import React, { useLayoutEffect, useRef } from "react";
import { Nav } from "@douyinfe/semi-ui";
import { cn } from "../../lib/cn";
import type { SectionKey } from "../../lib/useRouteState";

export interface AppSidebarProps {
  activeSection: SectionKey;
  onSectionChange: (section: SectionKey) => void;
}

// overview / workbench / tasks 三个 section 都落到「任务中心」这一项上。
const taskCenterSections: SectionKey[] = ["overview", "workbench", "tasks"];

const navigationItems = [
  { key: "tasks", icon: ListChecks, label: "任务中心" },
  { key: "schedules", icon: Clock, label: "归档计划" },
  { key: "gallery", icon: Images, label: "媒体归档" },
  { key: "tweets", icon: FileText, label: "archive.title" },
  { key: "settings", icon: Settings, label: "系统配置" },
] as const;

export function AppSidebar({
  activeSection,
  onSectionChange,
}: AppSidebarProps) {
  useTranslation();
  const currentKey = taskCenterSections.includes(activeSection) ? "tasks" : activeSection;
  const sidebar = useRef<HTMLElement>(null);

  useLayoutEffect(() => {
    sidebar.current?.querySelector('[role="menu"]')?.setAttribute("aria-label", t("主导航"));
    sidebar.current?.querySelectorAll('[role="menuitem"]').forEach((element, index) => {
      if (navigationItems[index]?.key === currentKey) element.setAttribute("aria-current", "page"); else element.removeAttribute("aria-current");
    });
  });

  return (
    <aside ref={sidebar} className="app-sidebar flex min-h-full w-full shrink-0 flex-col justify-between border-r border-line bg-surface select-none lg:w-56">
      <div className="sidebar-content">
        {/* 品牌区 */}
        <div className="sidebar-brand flex items-center gap-2.5">
          <img src="/icon.svg" alt="" className="size-8 shrink-0" />
          <div className="min-w-0">
            <div className="truncate text-sm font-bold tracking-tight text-fg">open-Xdownload</div>
            <p className="text-xs text-fg-muted font-medium truncate">{t("推文多媒体归档工作台")}</p>
          </div>
        </div>

        <Nav aria-label={t("主导航")} selectedKeys={[currentKey]} onSelect={data => onSectionChange(data.itemKey as SectionKey)} items={navigationItems.map(({ key, label, icon: Icon }) => ({ itemKey: key, text: t(label), icon: <Icon className="size-[18px] shrink-0" /> }))} style={{ border: 0, width: "100%" }} />

      </div>

    </aside>
  );
}

export function AppMobileNavigation({ activeSection, onSectionChange }: AppSidebarProps) {
  useTranslation();
  const currentKey = taskCenterSections.includes(activeSection) ? "tasks" : activeSection;

  return (
    <nav className="app-mobile-nav md:hidden" aria-label={t("主导航")}>
      {navigationItems.map(({ key, label, icon: Icon }) => (
        <button
          key={key}
          type="button"
          className="app-mobile-nav-item"
          aria-label={t(label)}
          aria-current={currentKey === key ? "page" : undefined}
          onClick={() => onSectionChange(key)}
        >
          <span className="app-mobile-nav-icon"><Icon size={20} /></span>
          <span className="app-mobile-nav-label">{t(label)}</span>
        </button>
      ))}
    </nav>
  );
}
