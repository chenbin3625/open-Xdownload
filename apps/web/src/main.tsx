import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CheckCircle2,
  CircleAlert,
  Clipboard,
  Download,
  Folder,
  ListRestart,
  LoaderCircle,
  Pause,
  Play,
  RefreshCw,
  Save,
  Settings,
  SquareX,
} from "lucide-react";
import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  AppConfig,
  DownloadRecord,
  FailedMedia,
  Job,
  JobKind,
  cancelJob,
  createJob,
  createMediaDownload,
  formatBytes,
  getDashboard,
  parseTweetLink,
  retryJob,
  updateConfig,
} from "./lib/api";
import "./styles.css";

const queryClient = new QueryClient();

function Root() {
  return (
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  );
}

function App() {
  const queryClient = useQueryClient();
  const dashboard = useQuery({ queryKey: ["dashboard"], queryFn: getDashboard });
  const data = dashboard.data;

  useEffect(() => {
    const events = new EventSource("/api/events");
    events.onmessage = () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    };
    return () => events.close();
  }, [queryClient]);

  const stats = useMemo(() => {
    const jobs = data?.jobs ?? [];
    return {
      active: jobs.filter((job) => ["pending", "resolving", "downloading"].includes(job.status)).length,
      failed: jobs.filter((job) => job.status === "failed").length,
      completed: jobs.filter((job) => job.status === "completed").length,
    };
  }, [data]);

  return (
    <main className="app-shell">
      <aside className="rail" aria-label="导航">
        <div className="brand">XD</div>
        <button className="rail-button active" title="工作台" aria-label="工作台">
          <Download size={20} />
        </button>
        <button className="rail-button" title="任务" aria-label="任务">
          <ListRestart size={20} />
        </button>
        <button className="rail-button" title="配置" aria-label="配置">
          <Settings size={20} />
        </button>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <div>
            <h1>open-Xdownload</h1>
            <p>127.0.0.1:8787</p>
          </div>
          <div className="status-strip">
            <StatusPill label="进行中" value={stats.active} />
            <StatusPill label="完成" value={stats.completed} tone="ok" />
            <StatusPill label="失败" value={stats.failed} tone="bad" />
          </div>
        </header>

        {dashboard.isLoading ? (
          <div className="loading">
            <LoaderCircle className="spin" size={26} />
          </div>
        ) : dashboard.isError ? (
          <div className="notice error">{dashboard.error.message}</div>
        ) : data ? (
          <div className="layout-grid">
            <section className="panel primary-panel">
              <TweetParser />
            </section>
            <section className="panel">
              <QuickDownload />
            </section>
            <section className="panel wide">
              <JobTable jobs={data.jobs} />
            </section>
            <section className="panel">
              <ConfigForm config={data.config} />
            </section>
            <section className="panel wide">
              <Library downloads={data.downloads} failed={data.failed} />
            </section>
          </div>
        ) : null}
      </section>
    </main>
  );
}

function StatusPill({ label, value, tone }: { label: string; value: number; tone?: "ok" | "bad" }) {
  return (
    <div className={`status-pill ${tone ?? ""}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function TweetParser() {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const [parsed, setParsed] = useState<Awaited<ReturnType<typeof parseTweetLink>> | null>(null);
  const parseMutation = useMutation({
    mutationFn: parseTweetLink,
    onSuccess: setParsed,
  });
  const jobMutation = useMutation({
    mutationFn: () => createJob("tweet_link", url, parsed?.id ? `Tweet ${parsed.id}` : "推文任务"),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });

  return (
    <div className="stack">
      <div className="panel-title">
        <h2>链接解析</h2>
        <button
          className="icon-button"
          title="解析"
          aria-label="解析"
          onClick={() => parseMutation.mutate(url)}
          disabled={!url || parseMutation.isPending}
        >
          {parseMutation.isPending ? <LoaderCircle className="spin" size={18} /> : <Play size={18} />}
        </button>
      </div>
      <div className="input-row">
        <input
          value={url}
          onChange={(event) => setUrl(event.target.value)}
          placeholder="https://x.com/user/status/123"
        />
        <button
          className="command-button"
          onClick={() => jobMutation.mutate()}
          disabled={!url || jobMutation.isPending}
        >
          <Download size={17} />
          下载
        </button>
      </div>
      {parseMutation.isError ? <div className="notice error">{parseMutation.error.message}</div> : null}
      {jobMutation.isError ? <div className="notice error">{jobMutation.error.message}</div> : null}
      {parsed ? (
        <div className="tweet-preview">
          <div className="tweet-meta">
            <span>@{parsed.author.screenName || "unknown"}</span>
            <span>{parsed.id}</span>
            <button
              className="icon-button ghost"
              title="复制"
              aria-label="复制"
              onClick={() => navigator.clipboard.writeText(parsed.url)}
            >
              <Clipboard size={16} />
            </button>
          </div>
          <p>{parsed.text}</p>
          <MediaList media={parsed.media} />
        </div>
      ) : null}
    </div>
  );
}

function MediaList({ media }: { media: Awaited<ReturnType<typeof parseTweetLink>>["media"] }) {
  if (media.length === 0) {
    return <div className="notice">等待 X GraphQL 详情接入</div>;
  }
  return (
    <div className="media-list">
      {media.map((item) => (
        <div className="media-row" key={item.id || item.bestUrl}>
          <span>{item.type}</span>
          <code>{item.bestUrl || item.url}</code>
          <button
            className="icon-button ghost"
            title="复制"
            aria-label="复制"
            onClick={() => navigator.clipboard.writeText(item.bestUrl || item.url)}
          >
            <Clipboard size={15} />
          </button>
        </div>
      ))}
    </div>
  );
}

function QuickDownload() {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const mutation = useMutation({
    mutationFn: createMediaDownload,
    onSuccess: () => {
      setUrl("");
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
  return (
    <div className="stack">
      <div className="panel-title">
        <h2>媒体下载</h2>
        <button
          className="icon-button"
          title="创建任务"
          aria-label="创建任务"
          onClick={() => mutation.mutate(url)}
          disabled={!url || mutation.isPending}
        >
          {mutation.isPending ? <LoaderCircle className="spin" size={18} /> : <Download size={18} />}
        </button>
      </div>
      <textarea
        value={url}
        onChange={(event) => setUrl(event.target.value)}
        placeholder="https://video.twimg.com/..."
      />
      {mutation.isError ? <div className="notice error">{mutation.error.message}</div> : null}
    </div>
  );
}

function JobTable({ jobs }: { jobs: Job[] }) {
  const queryClient = useQueryClient();
  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["dashboard"] }),
  });
  const cancel = useMutation({
    mutationFn: cancelJob,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["dashboard"] }),
  });

  return (
    <div className="stack">
      <div className="panel-title">
        <h2>任务中心</h2>
        <button
          className="icon-button ghost"
          title="刷新"
          aria-label="刷新"
          onClick={() => queryClient.invalidateQueries({ queryKey: ["dashboard"] })}
        >
          <RefreshCw size={17} />
        </button>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>状态</th>
              <th>任务</th>
              <th>进度</th>
              <th>消息</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {jobs.length === 0 ? (
              <tr>
                <td colSpan={5} className="empty-cell">
                  暂无任务
                </td>
              </tr>
            ) : (
              jobs.map((job) => (
                <tr key={job.id}>
                  <td>
                    <JobBadge status={job.status} />
                  </td>
                  <td>
                    <div className="job-title">{job.title || kindLabel(job.kind)}</div>
                    <div className="muted-line">{job.input}</div>
                  </td>
                  <td>
                    <div className="progress">
                      <span style={{ width: `${Math.round(job.progress * 100)}%` }} />
                    </div>
                  </td>
                  <td>
                    <div className={job.error ? "error-text" : ""}>{job.error || job.message}</div>
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="icon-button ghost"
                        title="取消"
                        aria-label="取消"
                        disabled={!["pending", "resolving", "downloading"].includes(job.status)}
                        onClick={() => cancel.mutate(job.id)}
                      >
                        <SquareX size={16} />
                      </button>
                      <button
                        className="icon-button ghost"
                        title="重试"
                        aria-label="重试"
                        disabled={job.status !== "failed"}
                        onClick={() => retry.mutate(job.id)}
                      >
                        <ListRestart size={16} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function JobBadge({ status }: { status: Job["status"] }) {
  const icon = {
    pending: <Pause size={14} />,
    resolving: <LoaderCircle className="spin" size={14} />,
    downloading: <Download size={14} />,
    completed: <CheckCircle2 size={14} />,
    failed: <CircleAlert size={14} />,
    canceled: <SquareX size={14} />,
  }[status];
  return (
    <span className={`job-badge ${status}`}>
      {icon}
      {statusLabel(status)}
    </span>
  );
}

function ConfigForm({ config }: { config: AppConfig }) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(config);
  useEffect(() => setDraft(config), [config]);
  const mutation = useMutation({
    mutationFn: updateConfig,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["dashboard"] }),
  });
  return (
    <div className="stack">
      <div className="panel-title">
        <h2>配置</h2>
        <button
          className="icon-button"
          title="保存"
          aria-label="保存"
          onClick={() => mutation.mutate(draft)}
          disabled={mutation.isPending}
        >
          {mutation.isPending ? <LoaderCircle className="spin" size={18} /> : <Save size={18} />}
        </button>
      </div>
      <label>
        <span>目录</span>
        <div className="input-with-icon">
          <Folder size={16} />
          <input
            value={draft.downloadDir}
            onChange={(event) => setDraft({ ...draft, downloadDir: event.target.value })}
          />
        </div>
      </label>
      <label>
        <span>并发</span>
        <input
          type="number"
          min={1}
          max={64}
          value={draft.maxConcurrency}
          onChange={(event) => setDraft({ ...draft, maxConcurrency: Number(event.target.value) })}
        />
      </label>
      <label>
        <span>auth_token</span>
        <input
          value={draft.authToken ?? ""}
          onChange={(event) => setDraft({ ...draft, authToken: event.target.value })}
        />
      </label>
      <label>
        <span>ct0</span>
        <input
          value={draft.csrfToken ?? ""}
          onChange={(event) => setDraft({ ...draft, csrfToken: event.target.value })}
        />
      </label>
      <label className="toggle-row">
        <span>失败重试</span>
        <input
          type="checkbox"
          checked={draft.autoRetryFailed}
          onChange={(event) => setDraft({ ...draft, autoRetryFailed: event.target.checked })}
        />
      </label>
      {mutation.isError ? <div className="notice error">{mutation.error.message}</div> : null}
    </div>
  );
}

function Library({ downloads, failed }: { downloads: DownloadRecord[]; failed: FailedMedia[] }) {
  return (
    <div className="library-grid">
      <div className="stack">
        <div className="panel-title">
          <h2>下载记录</h2>
        </div>
        <div className="record-list">
          {downloads.length === 0 ? (
            <div className="empty-cell">暂无记录</div>
          ) : (
            downloads.map((item) => (
              <div className="record-row" key={item.id}>
                <div>
                  <strong>{item.filePath}</strong>
                  <span>{item.mediaUrl}</span>
                </div>
                <em>{formatBytes(item.bytes)}</em>
              </div>
            ))
          )}
        </div>
      </div>
      <div className="stack">
        <div className="panel-title">
          <h2>失败记录</h2>
        </div>
        <div className="record-list">
          {failed.length === 0 ? (
            <div className="empty-cell">暂无失败</div>
          ) : (
            failed.map((item) => (
              <div className="record-row failed" key={item.id}>
                <div>
                  <strong>{item.error}</strong>
                  <span>{item.mediaUrl}</span>
                </div>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
}

function kindLabel(kind: JobKind) {
  return {
    tweet_link: "推文链接",
    media_url: "媒体地址",
    user: "用户",
    list: "列表",
    following: "关注",
  }[kind];
}

function statusLabel(status: Job["status"]) {
  return {
    pending: "排队",
    resolving: "解析",
    downloading: "下载",
    completed: "完成",
    failed: "失败",
    canceled: "取消",
  }[status];
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
);
