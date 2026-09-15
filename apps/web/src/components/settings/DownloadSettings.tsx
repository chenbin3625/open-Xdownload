import { HelpCircle } from "lucide-react";
import React from "react";
import type { AppConfig, FileNamingMode } from "../../lib/api";
import { Select } from "../ui/Controls";
import { Switch } from "../ui/Controls";
import { Field, Input, NumberInput } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";

export const fileNamingOptions: Array<{ value: FileNamingMode; label: string }> = [
  { value: "user_tweet", label: "用户名 + 用户 ID + 推文" },
  { value: "tweet_text", label: "仅推文" },
];

export const downloadSettingsTips = {
  proxy: (
    <div className="space-y-1 text-xs">
      <div>支持 http、https、socks5、socks5h，例如 http://127.0.0.1:7890。</div>
      <div>如需账号密码，可写成 socks5://user:password@127.0.0.1:1080。</div>
      <div>用户名或密码里的 @、:、/、% 需要 URL 编码；包含账号密码时会随配置保存在本地。</div>
    </div>
  ),
  maxFilenameLength: "限制保存到磁盘或远程存储的文件名长度，长推文文件名会自动截断。",
  fileNaming: "影响新下载文件的命名方式，已下载文件不会被重命名。",
  autoRetryFailed: "批量归档结束后，自动再次处理失败推文队列。",
  autoFollowProtected: "遇到未关注的保护账号时，使用已配置 Cookie 尝试发起关注后再归档。",
  includeNestedTweetMedia: "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。",
  incrementalArchive: (
    <div className="space-y-1 text-xs">
      <div>开启后重复归档从上次成功位置继续，节省 X API 配额。</div>
      <div>默认关闭：每次全量扫描时间线，已存在媒体自动跳过，并顺带补齐历史视频缺失的封面。</div>
    </div>
  ),
};

function TipIcon({ content }: { content: React.ReactNode }) {
  return (
    <Tooltip content={content}>
      <span className="inline-flex cursor-help text-fg-subtle hover:text-fg-muted">
        <HelpCircle className="size-3.5" />
      </span>
    </Tooltip>
  );
}

export function DownloadSettingsFields({
  draft,
  onChange,
  onAuthChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  onAuthChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <div className="space-y-4">
      {/* 基础输入网格 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <div className="sm:col-span-2">
          <Field
            label="网络代理 (Proxy)"
            tooltip={<TipIcon content={downloadSettingsTips.proxy} />}
          >
            <Input
              value={draft.proxyUrl}
              onChange={(event) =>
                onAuthChange((current) => ({ ...current, proxyUrl: event.target.value }))
              }
              placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:1080"
            />
          </Field>
        </div>

        <div>
          <Field
            label="最大文件名长度"
            tooltip={<TipIcon content={downloadSettingsTips.maxFilenameLength} />}
          >
            <NumberInput
              min={16}
              max={240}
              value={draft.maxFilenameLength}
              onChange={(value) =>
                onChange((current) => ({ ...current, maxFilenameLength: value ?? 120 }))
              }
            />
          </Field>
        </div>

        <div className="sm:col-span-2">
          <Field
            label="文件命名策略"
            tooltip={<TipIcon content={downloadSettingsTips.fileNaming} />}
          >
            <Select
              value={draft.fileNamingMode}
              options={fileNamingOptions}
              onChange={(value) =>
                onChange((current) => ({
                  ...current,
                  fileNamingMode: value as FileNamingMode,
                }))
              }
            />
          </Field>
        </div>
      </div>

      {/* 功能开关卡片网格 */}
      <div className="grid grid-cols-1 gap-3 pt-2 sm:grid-cols-2 lg:grid-cols-4 border-t border-line">
        <div className="flex items-center justify-between rounded-control border border-line bg-surface-muted/50 p-3">
          <div className="min-w-0 pr-2">
            <div className="flex items-center gap-1.5 text-xs font-medium text-fg">
              <span>失败推文自动重试</span>
              <TipIcon content={downloadSettingsTips.autoRetryFailed} />
            </div>
            <p className="text-[11px] text-fg-subtle mt-0.5">归档完成后自动补救</p>
          </div>
          <Switch
            checked={draft.autoRetryFailed}
            onChange={(checked) =>
              onChange((current) => ({ ...current, autoRetryFailed: checked }))
            }
            ariaLabel="切换失败推文自动重试"
          />
        </div>

        <div className="flex items-center justify-between rounded-control border border-line bg-surface-muted/50 p-3">
          <div className="min-w-0 pr-2">
            <div className="flex items-center gap-1.5 text-xs font-medium text-fg">
              <span>保护账号自动关注</span>
              <TipIcon content={downloadSettingsTips.autoFollowProtected} />
            </div>
            <p className="text-[11px] text-fg-subtle mt-0.5">自动发起关注申请</p>
          </div>
          <Switch
            checked={draft.autoFollowProtected}
            onChange={(checked) =>
              onChange((current) => ({ ...current, autoFollowProtected: checked }))
            }
            ariaLabel="切换保护账号自动关注"
          />
        </div>

        <div className="flex items-center justify-between rounded-control border border-line bg-surface-muted/50 p-3">
          <div className="min-w-0 pr-2">
            <div className="flex items-center gap-1.5 text-xs font-medium text-fg">
              <span>引用/转推媒体入库</span>
              <TipIcon content={downloadSettingsTips.includeNestedTweetMedia} />
            </div>
            <p className="text-[11px] text-fg-subtle mt-0.5">收集推文中包含的嵌套媒体</p>
          </div>
          <Switch
            checked={draft.includeNestedTweetMedia}
            onChange={(checked) =>
              onChange((current) => ({ ...current, includeNestedTweetMedia: checked }))
            }
            ariaLabel="切换引用/转推媒体入库"
          />
        </div>

        <div className="flex items-center justify-between rounded-control border border-line bg-surface-muted/50 p-3">
          <div className="min-w-0 pr-2">
            <div className="flex items-center gap-1.5 text-xs font-medium text-fg">
              <span>增量归档模式</span>
              <TipIcon content={downloadSettingsTips.incrementalArchive} />
            </div>
            <p className="text-[11px] text-fg-subtle mt-0.5">从上次记录处断点续扫</p>
          </div>
          <Switch
            checked={draft.incrementalArchive}
            onChange={(checked) =>
              onChange((current) => ({ ...current, incrementalArchive: checked }))
            }
            ariaLabel="切换增量归档模式"
          />
        </div>
      </div>
    </div>
  );
}
