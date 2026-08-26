import { useQuery } from "@tanstack/react-query";
import React from "react";
import { configQueryRoot, getConfig } from "../../lib/api";
import { ConfigForm } from "./ConfigForm";

/**
 * 独立为默认导出模块，供 App 用 React.lazy 按需加载。
 * 首屏配置来自 HTML bootstrap；缓存未命中时才打 /api/config。
 */
export default function SettingsPage() {
  const configQuery = useQuery({
    queryKey: configQueryRoot,
    queryFn: ({ signal }) => getConfig(signal),
    staleTime: 15_000,
  });

  if (configQuery.isLoading && !configQuery.data) {
    return (
      <div className="settings-page">
        <div className="shell-skeleton-block shell-skeleton-block-tall" />
      </div>
    );
  }

  if (configQuery.isError || !configQuery.data) {
    return (
      <div className="settings-page">
        <p className="shell-error">
          配置加载失败：{configQuery.error instanceof Error ? configQuery.error.message : "请稍后重试"}
        </p>
      </div>
    );
  }

  return (
    <div className="settings-page">
      <ConfigForm config={configQuery.data} />
    </div>
  );
}
