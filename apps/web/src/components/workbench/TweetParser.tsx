import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useRef, useState } from "react";
import { createJob, parseTweetLink, type TweetData } from "../../lib/api";
import { getErrorMessage, mediaTypeLabel } from "../../lib/format";
import { toast } from "../../lib/toast";
import { prependJobsToCaches } from "../../lib/useDashboardEvents";
import { CopyTextButton } from "../common/ShellUI";

export const TweetParser = React.memo(function TweetParser() {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const [parsed, setParsed] = useState<TweetData | null>(null);
  const [parsedSourceUrl, setParsedSourceUrl] = useState("");
  const latestParseUrl = useRef("");
  const parsedHasMedia = parsed !== null && parsed.media.length > 0;

  const parseMutation = useMutation({
    mutationFn: (targetUrl: string) => parseTweetLink(targetUrl),
    onSuccess: (data, targetUrl) => {
      if (targetUrl !== latestParseUrl.current) {
        return;
      }
      setParsed(data);
      setParsedSourceUrl(targetUrl);
      toast("解析完成", { description: `发现 ${data.media.length} 个媒体` });
    },
    onError: (error) => {
      toast("解析失败", { description: getErrorMessage(error), tone: "err" });
    },
  });

  const jobMutation = useMutation({
    mutationFn: () => createJob("tweet_link", parsedSourceUrl, parsed?.id ? `Tweet ${parsed.id}` : "推文任务"),
    onSuccess: (job) => {
      prependJobsToCaches(queryClient, [job]);
      toast("下载任务已创建");
    },
    onError: (error) => {
      toast("创建失败", { description: getErrorMessage(error), tone: "err" });
    },
  });

  function handleParse(event?: React.FormEvent) {
    event?.preventDefault();
    const trimmed = url.trim();
    if (!trimmed) return;
    latestParseUrl.current = trimmed;
    setParsed(null);
    setParsedSourceUrl("");
    parseMutation.mutate(trimmed);
  }

  return (
    <div className="parser-stack">
      <form className="parser-form" onSubmit={handleParse}>
        <label className="visually-hidden" htmlFor="tweet-url">推文链接</label>
        <input
          id="tweet-url"
          className="parser-input"
          value={url}
          placeholder="https://x.com/user/status/123"
          autoComplete="off"
          onChange={(event) => {
            const nextUrl = event.target.value;
            latestParseUrl.current = nextUrl.trim();
            setUrl(nextUrl);
            setParsed(null);
            setParsedSourceUrl("");
          }}
        />
        <button type="submit" className="shell-primary-btn" disabled={parseMutation.isPending}>
          {parseMutation.isPending ? "解析中…" : "解析"}
        </button>
      </form>

      {parseMutation.isPending && !parsed ? <div className="shell-skeleton-block" /> : null}

      {parsed ? (
        <div className="parser-result">
          <dl className="job-meta parser-meta">
            <div>
              <dt>作者</dt>
              <dd>@{parsed.author.screenName || "unknown"}</dd>
            </div>
            <div>
              <dt>推文</dt>
              <dd>{parsed.id}</dd>
            </div>
            <div>
              <dt>链接</dt>
              <dd><CopyTextButton label="复制链接" value={parsed.url} /></dd>
            </div>
          </dl>
          <p className="parser-text">{parsed.text || "无正文"}</p>
          <MediaList media={parsed.media} />
          <div className="parser-actions">
            <button
              type="button"
              className="shell-primary-btn"
              disabled={!parsedHasMedia || !parsedSourceUrl || jobMutation.isPending}
              onClick={() => jobMutation.mutate()}
            >
              {jobMutation.isPending ? "创建中…" : "下载媒体"}
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
});

export function MediaList({ media }: { media: TweetData["media"] }) {
  const [page, setPage] = useState(1);
  const pageSize = 5;
  const totalPages = Math.max(1, Math.ceil(media.length / pageSize));
  const current = Math.min(page, totalPages);
  const items = media.slice((current - 1) * pageSize, current * pageSize);

  if (media.length === 0) {
    return <p className="job-empty">未发现可下载媒体</p>;
  }

  return (
    <div className="media-list">
      <ul className="job-file-list">
        {items.map((item) => {
          const mediaUrl = item.bestUrl || item.url;
          return (
            <li key={item.id}>
              <div className="job-file-main">
                <span className="job-kind-tag">{mediaTypeLabel(item.type)}</span>
                <code className="job-ellipsis" title={mediaUrl}>{mediaUrl}</code>
              </div>
              <CopyTextButton label="复制媒体地址" value={mediaUrl} />
            </li>
          );
        })}
      </ul>
      {totalPages > 1 ? (
        <div className="shell-pagination">
          <button type="button" className="shell-page-btn" disabled={current <= 1} onClick={() => setPage(current - 1)}>
            上一页
          </button>
          <span>{current}/{totalPages}</span>
          <button type="button" className="shell-page-btn" disabled={current >= totalPages} onClick={() => setPage(current + 1)}>
            下一页
          </button>
        </div>
      ) : null}
    </div>
  );
}
