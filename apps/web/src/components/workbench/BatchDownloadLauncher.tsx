import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useMemo, useState } from "react";
import {
  archiveScheduleQueryRoot,
  createArchiveSchedule,
  createJobsBatch,
  type JobRequest,
} from "../../lib/api";
import { formatIntervalMinutes, getErrorMessage, kindLabel } from "../../lib/format";
import { toast } from "../../lib/toast";
import { prependJobsToCaches } from "../../lib/useDashboardEvents";

const targetTabs = [
  { key: "users", label: "用户", placeholder: "elonmusk\n1234567" },
  { key: "lists", label: "列表", placeholder: "8901234" },
  { key: "following", label: "关注", placeholder: "567890\n@screen_name" },
] as const;

type TargetTab = (typeof targetTabs)[number]["key"];

export function BatchDownloadLauncher() {
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState<TargetTab>("users");
  const [users, setUsers] = useState("");
  const [lists, setLists] = useState("");
  const [following, setFollowing] = useState("");
  const [scheduleName, setScheduleName] = useState("");
  const [intervalMinutes, setIntervalMinutes] = useState(360);
  const [previewPage, setPreviewPage] = useState(1);
  const items = useMemo(() => buildBatchDownloadItems(users, lists, following), [users, lists, following]);
  const previewPageSize = 6;
  const previewPages = Math.max(1, Math.ceil(items.length / previewPageSize));
  const previewCurrent = Math.min(previewPage, previewPages);
  const previewItems = items.slice((previewCurrent - 1) * previewPageSize, previewCurrent * previewPageSize);

  const createJobs = useMutation({
    mutationFn: () => createJobsBatch({ items }),
    onSuccess: (data) => {
      setUsers("");
      setLists("");
      setFollowing("");
      setPreviewPage(1);
      prependJobsToCaches(queryClient, data);
      toast("批量任务已创建", { description: `已创建 ${data.length} 个任务` });
    },
    onError: (error) => {
      toast("创建失败", { description: getErrorMessage(error), tone: "err" });
    },
  });

  const createSchedule = useMutation({
    mutationFn: () =>
      createArchiveSchedule({
        name: scheduleName.trim() || defaultArchiveScheduleName(items),
        enabled: true,
        intervalMinutes,
        items,
      }),
    onSuccess: (schedule) => {
      setScheduleName("");
      queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
      toast("定时计划已保存", {
        description: `${schedule.name} · ${formatIntervalMinutes(schedule.intervalMinutes)}`,
      });
    },
    onError: (error) => {
      toast("保存失败", { description: getErrorMessage(error), tone: "err" });
    },
  });

  const busy = createJobs.isPending || createSchedule.isPending;
  const activePlaceholder = targetTabs.find((tab) => tab.key === activeTab)?.placeholder ?? "";
  const activeValue = activeTab === "users" ? users : activeTab === "lists" ? lists : following;
  const setActiveValue = activeTab === "users" ? setUsers : activeTab === "lists" ? setLists : setFollowing;

  return (
    <div className="batch-stack">
      <div className="batch-toolbar">
        <span className="batch-count">待创建 {items.length}</span>
        <label className="batch-field">
          <span className="visually-hidden">计划名称</span>
          <input
            className="parser-input"
            value={scheduleName}
            placeholder="计划名称"
            onChange={(event) => setScheduleName(event.target.value)}
          />
        </label>
        <label className="batch-interval">
          每
          <input
            type="number"
            min={5}
            max={43200}
            value={intervalMinutes}
            onChange={(event) => setIntervalMinutes(Number(event.target.value) || 5)}
          />
          分钟
        </label>
        <button
          type="button"
          className="job-text-btn"
          disabled={items.length === 0 || busy}
          onClick={() => createSchedule.mutate()}
        >
          {createSchedule.isPending ? "保存中…" : "保存计划"}
        </button>
        <button
          type="button"
          className="shell-primary-btn"
          disabled={items.length === 0 || busy}
          onClick={() => createJobs.mutate()}
        >
          {createJobs.isPending ? "创建中…" : "批量下载"}
        </button>
      </div>

      <div className="batch-launcher-grid">
        <div className="batch-input-pane">
          <div className="batch-tabs" role="tablist" aria-label="批量目标">
            {targetTabs.map((tab) => (
              <button
                key={tab.key}
                type="button"
                role="tab"
                aria-selected={activeTab === tab.key}
                className={activeTab === tab.key ? "batch-tab is-active" : "batch-tab"}
                onClick={() => setActiveTab(tab.key)}
              >
                {tab.label}
              </button>
            ))}
          </div>
          <textarea
            className="batch-textarea"
            value={activeValue}
            placeholder={activePlaceholder}
            rows={8}
            onChange={(event) => {
              setActiveValue(event.target.value);
              setPreviewPage(1);
            }}
          />
        </div>
        <div className="batch-preview-pane">
          <div className="batch-preview-heading">
            <strong>任务预览</strong>
            <span>{items.length > 0 ? `准备创建 ${items.length} 个任务` : "输入目标后生成预览"}</span>
          </div>
          {items.length === 0 ? (
            <p className="job-empty">暂无待创建任务</p>
          ) : (
            <ul className="batch-preview-list">
              {previewItems.map((item) => (
                <li key={`${item.kind}:${item.input}`}>
                  <span className="job-kind-tag">{kindLabel(item.kind)}</span>
                  <span className="job-ellipsis" title={item.input}>{item.input}</span>
                </li>
              ))}
            </ul>
          )}
          {previewPages > 1 ? (
            <div className="shell-pagination">
              <button
                type="button"
                className="shell-page-btn"
                disabled={previewCurrent <= 1}
                onClick={() => setPreviewPage(previewCurrent - 1)}
              >
                上一页
              </button>
              <span>
                {previewCurrent}/{previewPages}
              </span>
              <button
                type="button"
                className="shell-page-btn"
                disabled={previewCurrent >= previewPages}
                onClick={() => setPreviewPage(previewCurrent + 1)}
              >
                下一页
              </button>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
export function buildBatchDownloadItems(users: string, lists: string, following: string): JobRequest[] {
  const items: JobRequest[] = [];
  for (const input of parseTargets(users)) {
    items.push({ kind: "user", input, title: `用户 ${displayDownloadTarget(input)}` });
  }
  for (const input of parseTargets(lists)) {
    items.push({ kind: "list", input, title: `列表 ${input}` });
  }
  for (const input of parseTargets(following)) {
    items.push({ kind: "following", input, title: `关注 ${displayDownloadTarget(input)}` });
  }
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = `${item.kind}:${item.input.toLowerCase()}`;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

export function defaultArchiveScheduleName(items: JobRequest[]) {
  if (items.length === 0) {
    return "批量归档计划";
  }
  const first = items[0];
  return `${kindLabel(first.kind)} ${first.input}${items.length > 1 ? ` 等 ${items.length} 个目标` : ""}`;
}

export function parseTargets(value: string) {
  return value
    .split(/[\n,，\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function displayDownloadTarget(input: string) {
  if (input.startsWith("@") || /^\d+$/.test(input)) {
    return input;
  }
  return `@${input}`;
}
