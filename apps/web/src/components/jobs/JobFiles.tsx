import React, { useState } from "react";
import { formatBytes, type DownloadRecord, type FailedMedia } from "../../lib/api";
import { CopyTextButton } from "../common/ShellUI";

export function JobFiles({ downloads, failed }: { downloads: DownloadRecord[]; failed: FailedMedia[] }) {
  const [tab, setTab] = useState<"downloads" | "failed">(failed.length > 0 && downloads.length === 0 ? "failed" : "downloads");
  const total = downloads.length + failed.length;
  if (total === 0) {
    return <p className="job-empty">暂无文件记录</p>;
  }

  const items = tab === "downloads" ? downloads : failed;
  return (
    <div className="job-files">
      <div className="job-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "downloads"}
          className={tab === "downloads" ? "is-active" : undefined}
          onClick={() => setTab("downloads")}
        >
          已下载 {downloads.length}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "failed"}
          className={tab === "failed" ? "is-active" : undefined}
          onClick={() => setTab("failed")}
        >
          失败 {failed.length}
        </button>
      </div>
      {items.length === 0 ? (
        <p className="job-empty">{tab === "downloads" ? "暂无下载文件" : "暂无失败媒体"}</p>
      ) : (
        <ul className="job-file-list">
          {tab === "downloads"
            ? downloads.map((item) => (
                <li key={item.id}>
                  <div className="job-file-main">
                    <strong title={item.filePath}>{item.filePath}</strong>
                    <span>{formatBytes(item.bytes)}</span>
                    <span className="job-ellipsis" title={item.mediaUrl}>{item.mediaUrl}</span>
                  </div>
                  <CopyTextButton label="复制文件路径" value={item.filePath} />
                </li>
              ))
            : failed.map((item) => (
                <li key={item.id}>
                  <div className="job-file-main">
                    <strong className="is-danger" title={item.error}>{item.error}</strong>
                    <span className="job-ellipsis" title={item.mediaUrl}>{item.mediaUrl}</span>
                  </div>
                  <CopyTextButton label="复制媒体地址" value={item.mediaUrl} />
                </li>
              ))}
        </ul>
      )}
    </div>
  );
}
