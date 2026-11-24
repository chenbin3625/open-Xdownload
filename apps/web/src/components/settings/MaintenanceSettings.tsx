import React, { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Database,
  FileJson,
  HardDriveDownload,
  RefreshCw,
  ScanSearch,
  Sparkles,
  Upload,
} from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import {
  archiveScheduleQueryRoot,
  cleanupMaintenance,
  collectionsQueryRoot,
  formatBytes,
  getMaintenanceStats,
  importConfigBundle,
  maintenanceStatsQueryRoot,
  scanMaintenance,
  tagsQueryRoot,
  type MaintenanceScanResult,
} from "../../lib/api";
import { t, useTranslation } from "../../lib/i18n";
import { notifyError } from "../common/CommonUI";
import { Popconfirm } from "../semi/overlays";
import { toast } from "../semi/toast";

export function MaintenanceSettings() {
  useTranslation();
  const queryClient = useQueryClient();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [scanResult, setScanResult] = useState<MaintenanceScanResult | null>(null);

  const statsQuery = useQuery({
    queryKey: maintenanceStatsQueryRoot,
    queryFn: ({ signal }) => getMaintenanceStats(signal),
    retry: false,
    staleTime: 60_000,
  });

  const scanMutation = useMutation({
    mutationFn: scanMaintenance,
    onSuccess: (res) => {
      setScanResult(res);
      toast.success({
        message: t("库扫描完成"),
        description: t("断链 {{value0}} · 孤儿文件 {{value1}} · 重复组 {{value2}}", {
          value0: res.brokenCount,
          value1: res.orphanCount,
          value2: res.duplicateCount,
        }),
      });
    },
    onError: notifyError(t("扫描失败")),
  });

  const cleanupMutation = useMutation({
    mutationFn: cleanupMaintenance,
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: maintenanceStatsQueryRoot });
      toast.success({
        message: t("维护操作已完成"),
        description: t("处理 {{value0}} 项，释放 {{value1}}", {
          value0: res.affected,
          value1: formatBytes(res.reclaimedBytes),
        }),
      });
      scanMutation.mutate();
    },
    onError: notifyError(t("维护操作失败")),
  });

  const importMutation = useMutation({
    mutationFn: importConfigBundle,
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
      void queryClient.invalidateQueries({ queryKey: tagsQueryRoot });
      void queryClient.invalidateQueries({ queryKey: collectionsQueryRoot });
      toast.success({
        message: t("配置包已导入"),
        description: t("新增计划 {{value0}} · 标签 {{value1}} · 合集 {{value2}}", {
          value0: res.createdSchedules,
          value1: res.createdTags,
          value2: res.createdCollections,
        }),
      });
    },
    onError: notifyError(t("导入失败")),
  });

  const handleImportFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    try {
      const text = await file.text();
      const parsed = JSON.parse(text);
      importMutation.mutate(parsed);
    } catch {
      toast.error({ message: t("导入失败"), description: t("JSON 文件格式无效") });
    }
  };

  const stats = statsQuery.data && typeof statsQuery.data.totalTweets === "number" ? statsQuery.data : null;
  const diskUsedPct =
    stats && stats.diskAvailable && stats.diskTotalBytes > 0
      ? Math.min(100, Math.round(((stats.diskTotalBytes - stats.diskFreeBytes) / stats.diskTotalBytes) * 100))
      : 0;

  return (
    <section id="settings-maintenance" className="semi-settings-section space-y-4">
      <div className="settings-section-heading">
        <h2 className="settings-section-title">
          <Database size={16} className="shrink-0" />
          {t("存储统计与库维护")}
        </h2>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="tertiary"
            size="small"
            icon={<RefreshCw size={14} />}
            loading={statsQuery.isFetching}
            onClick={() => void statsQuery.refetch()}
          >
            {t("刷新统计")}
          </Button>
          <Button
            type="tertiary"
            size="small"
            icon={<ScanSearch size={14} />}
            loading={scanMutation.isPending}
            onClick={() => scanMutation.mutate()}
          >
            {t("扫描异常与重复")}
          </Button>
        </div>
      </div>

      {stats && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 text-xs">
            <div className="rounded-card border border-line bg-surface-muted/40 p-3 space-y-1">
              <div className="text-fg-muted">{t("归档推文")}</div>
              <div className="text-lg font-semibold text-fg tabular-nums">{stats.totalTweets.toLocaleString()}</div>
            </div>
            <div className="rounded-card border border-line bg-surface-muted/40 p-3 space-y-1">
              <div className="text-fg-muted">{t("媒体文件")}</div>
              <div className="text-lg font-semibold text-fg tabular-nums">{stats.totalDownloads.toLocaleString()}</div>
              <div className="text-fg-subtle tabular-nums">{formatBytes(stats.totalBytes)}</div>
            </div>
            <div className="rounded-card border border-line bg-surface-muted/40 p-3 space-y-1">
              <div className="text-fg-muted">{t("数据库大小")}</div>
              <div className="text-lg font-semibold text-fg tabular-nums">{formatBytes(stats.databaseBytes)}</div>
            </div>
            <div className="rounded-card border border-line bg-surface-muted/40 p-3 space-y-1">
              <div className="text-fg-muted">{t("重复文件组")}</div>
              <div className="text-lg font-semibold text-fg tabular-nums">{stats.duplicateGroups}</div>
              <div className="text-fg-subtle tabular-nums">{t("可省")} {formatBytes(stats.duplicateBytes)}</div>
            </div>
          </div>

          {stats.diskAvailable && stats.diskTotalBytes > 0 && (
            <div className="rounded-card border border-line p-3 space-y-1.5 text-xs">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-semibold text-fg">{t("下载盘空间")} ({stats.downloadDir})</span>
                <span className="text-fg-muted tabular-nums">
                  {t("可用")} {formatBytes(stats.diskFreeBytes)} / {formatBytes(stats.diskTotalBytes)} ({diskUsedPct}%)
                </span>
              </div>
              <Progress percent={diskUsedPct} showInfo={false} />
            </div>
          )}

          {stats.byKind && (
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 text-xs">
              {(["images", "videos", "gifs", "other"] as const).map((key) => {
                const item = stats.byKind[key] ?? { count: 0, bytes: 0 };
                const label = key === "images" ? t("图片") : key === "videos" ? t("视频") : key === "gifs" ? "GIF" : t("其他");
                return (
                  <div key={key} className="rounded-card border border-line px-3 py-2 flex items-center justify-between">
                    <span className="text-fg-muted">{label}</span>
                    <span className="font-mono text-fg tabular-nums">{item.count} · {formatBytes(item.bytes)}</span>
                  </div>
                );
              })}
            </div>
          )}

          {stats.topUsers && stats.topUsers.length > 0 && (
            <div className="rounded-card border border-line p-3 space-y-2 text-xs">
              <div className="font-semibold text-fg">{t("空间占用 Top 账号")}</div>
              <div className="space-y-1.5">
                {stats.topUsers.slice(0, 5).map((u) => (
                  <div key={u.screenName} className="flex items-center justify-between gap-2">
                    <span className="truncate text-fg">@{u.screenName} {u.displayName ? `(${u.displayName})` : ""}</span>
                    <span className="shrink-0 font-mono text-fg-muted tabular-nums">
                      {u.mediaCount} · {formatBytes(u.totalBytes)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {scanResult && (
        <div className="rounded-card border border-line bg-surface-muted/40 p-3.5 space-y-3 text-xs">
          <div className="flex flex-wrap items-center gap-2 font-semibold text-fg">
            <Sparkles size={15} className="text-brand-500" />
            <span>{t("扫描结果")}</span>
          </div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
            <div className="rounded border border-line bg-surface p-2.5 space-y-2">
              <div className="font-semibold text-fg">{t("断链记录")}: {scanResult.brokenCount}</div>
              <p className="text-fg-subtle">{t("数据库有索引但磁盘文件已缺失")}</p>
              <Button
                size="small"
                type="tertiary"
                disabled={scanResult.brokenCount === 0 || cleanupMutation.isPending}
                onClick={() => cleanupMutation.mutate("prune_broken")}
              >
                {t("清理断链记录")}
              </Button>
            </div>
            <div className="rounded border border-line bg-surface p-2.5 space-y-2">
              <div className="font-semibold text-fg">{t("孤儿文件")}: {scanResult.orphanCount}</div>
              <p className="text-fg-subtle">{t("磁盘存在但未登记入库的媒体文件")}</p>
              <div className="flex flex-wrap gap-1.5">
                <Button
                  size="small"
                  type="tertiary"
                  disabled={scanResult.orphanCount === 0 || cleanupMutation.isPending}
                  onClick={() => cleanupMutation.mutate("import_orphans")}
                >
                  {t("导入入库")}
                </Button>
                <Popconfirm
                  title={t("删除孤儿文件")}
                  description={t("将从磁盘永久删除 {{value0}} 个未入库文件，确认继续？", { value0: scanResult.orphanCount })}
                  danger
                  disabled={scanResult.orphanCount === 0}
                  onConfirm={() => cleanupMutation.mutate("delete_orphans")}
                >
                  <Button
                    size="small"
                    type="danger"
                    disabled={scanResult.orphanCount === 0 || cleanupMutation.isPending}
                  >
                    {t("清理文件")}
                  </Button>
                </Popconfirm>
              </div>
            </div>
            <div className="rounded border border-line bg-surface p-2.5 space-y-2">
              <div className="font-semibold text-fg">
                {t("重复文件组")}: {scanResult.duplicateCount} ({formatBytes(scanResult.reclaimableBytes)})
              </div>
              <p className="text-fg-subtle">{t("相同 SHA-256 哈希文件可用硬链接合并节省空间")}</p>
              <Popconfirm
                title={t("硬链接去重")}
                description={t("将重复文件内容合并为同一 inode 硬链接，保留所有文件路径不受影响。")}
                disabled={scanResult.duplicateCount === 0}
                onConfirm={() => cleanupMutation.mutate("dedup_hardlink")}
              >
                <Button
                  size="small"
                  type="tertiary"
                  disabled={scanResult.duplicateCount === 0 || cleanupMutation.isPending}
                >
                  {t("硬链接去重")}
                </Button>
              </Popconfirm>
            </div>
          </div>
        </div>
      )}

      {/* 备份与配置迁移 */}
      <div className="flex flex-wrap items-center gap-2 pt-1">
        <a
          href="/api/maintenance/backup"
          download
          className="semi-button semi-button-light semi-button-tertiary semi-button-size-default"
        >
          <HardDriveDownload size={15} />
          <span>{t("下载数据库热备份 (.db)")}</span>
        </a>
        <a
          href="/api/maintenance/export-config"
          download
          className="semi-button semi-button-light semi-button-tertiary semi-button-size-default"
        >
          <FileJson size={15} />
          <span>{t("导出配置与计划 (.json)")}</span>
        </a>
        <Button
          type="tertiary"
          icon={<Upload size={15} />}
          loading={importMutation.isPending}
          onClick={() => fileInputRef.current?.click()}
        >
          {t("导入配置包 (.json)")}
        </Button>
        <input
          ref={fileInputRef}
          type="file"
          accept=".json,application/json"
          className="hidden"
          onChange={handleImportFile}
        />
      </div>
    </section>
  );
}
