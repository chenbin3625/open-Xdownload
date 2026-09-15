import {
  Clock,
  Download,
  Link as LinkIcon,
  List as ListIcon,
  Plus,
  Sparkles,
  User,
  UserPlus,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import {
  createArchiveSchedule,
  createJob,
  createJobsBatch,
  parseTweetLink,
  type ArchiveScheduleRequest,
  type JobKind,
  type JobRequest,
  type TweetData,
} from "../../lib/api";
import { notifyError } from "../common/CommonUI";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";
import { Button } from "../ui/Button";
import { Checkbox, Tabs, type TabItem } from "../ui/Controls";
import { Input, NumberInput, Textarea } from "../ui/Input";
import { Modal } from "../ui/Overlay";
import { Spinner } from "../ui/Spinner";
import { toast } from "../ui/Toast";

export interface CreateJobModalProps {
  open: boolean;
  onClose: () => void;
  initialInput?: string;
  initialKind?: JobKind | "schedule";
}

const tabKeys: string[] = ["user", "tweet_link", "list", "following"];

type BatchKind = Extract<JobKind, "user" | "list" | "following">;

const batchTabs: {
  kind: BatchKind;
  label: string;
  icon: React.ReactNode;
  hint: string;
  placeholder: string;
}[] = [
  {
    kind: "user",
    label: "用户归档",
    icon: <User className="size-4" />,
    hint: "支持用户名、@screen_name 或数字 ID，每行一个",
    placeholder: "elonmusk\n@sama\n44196397\nhttps://x.com/OpenAI",
  },
  {
    kind: "list",
    label: "列表归档",
    icon: <ListIcon className="size-4" />,
    hint: "输入 X 列表 ID 或完整列表 URL，自动获取列表成员推文媒体",
    placeholder: "1492019283\nhttps://x.com/i/lists/1647289190",
  },
  {
    kind: "following",
    label: "关注关系归档",
    icon: <UserPlus className="size-4" />,
    hint: "输入目标账号，自动获取其关注的所有账号并进行媒体归档",
    placeholder: "elonmusk\n@OpenAI",
  },
];

function parseLinesToItems(raw: string, kind: JobKind): JobRequest[] {
  const seen = new Set<string>();
  const items: JobRequest[] = [];
  const lines = raw.split(/[\r\n]+/);

  for (const rawLine of lines) {
    let line = rawLine.trim();
    if (!line) continue;

    // 清洗常见的输入前缀
    if (kind === "user" || kind === "following") {
      if (line.startsWith("https://x.com/") || line.startsWith("https://twitter.com/")) {
        const parts = line.split("/").filter(Boolean);
        line = parts[parts.length - 1] || line;
      }
      line = line.replace(/^@+/, "").trim();
    } else if (kind === "list") {
      const match = line.match(/(?:lists\/|^)(\d+)/);
      if (match) {
        line = match[1];
      }
    }

    if (!line || seen.has(line)) continue;
    seen.add(line);
    items.push({ kind, input: line });
  }

  return items;
}

export function CreateJobModal({
  open,
  onClose,
  initialInput = "",
  initialKind = "user",
}: CreateJobModalProps) {
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState<string>("user");

  const [tweetUrl, setTweetUrl] = useState("");
  const [batchInputs, setBatchInputs] = useState<Partial<Record<BatchKind, string>>>({});

  const [parsedTweet, setParsedTweet] = useState<TweetData | null>(null);

  const [isSchedule, setIsSchedule] = useState(false);
  const [scheduleName, setScheduleName] = useState("");
  const [intervalMinutes, setIntervalMinutes] = useState(360);

  useEffect(() => {
    if (!open) return;
    if (initialInput) {
      if (
        initialInput.includes("status/") ||
        initialInput.includes("x.com/") ||
        initialInput.includes("twitter.com/")
      ) {
        setActiveTab("tweet_link");
        setTweetUrl(initialInput);
      } else if (/^\d{5,}$/.test(initialInput.trim())) {
        setActiveTab("list");
        setBatchInputs({ list: initialInput });
      } else {
        setActiveTab("user");
        setBatchInputs({ user: initialInput });
      }
    } else {
      setActiveTab(tabKeys.includes(initialKind) ? initialKind : "user");
    }
    if (initialKind === "schedule") {
      setIsSchedule(true);
    }
  }, [open, initialInput, initialKind]);

  const currentBatchItems = useMemo(() => {
    if (activeTab === "tweet_link") return [];
    const raw = batchInputs[activeTab as BatchKind] ?? "";
    return parseLinesToItems(raw, activeTab as JobKind);
  }, [activeTab, batchInputs]);

  const parseMutation = useMutation({
    mutationFn: parseTweetLink,
    onSuccess: (data) => {
      setParsedTweet(data);
      toast.success({
        message: "推文解析成功",
        description: `包含 ${data.media.length} 个媒体，作者 @${data.author.screenName}`,
      });
    },
    onError: notifyError("解析失败"),
  });

  const createJobsMutation = useMutation({
    mutationFn: async () => {
      if (activeTab === "tweet_link") {
        const targetUrl = tweetUrl.trim();
        if (!targetUrl) throw new Error("请输入推文链接");
        const title = parsedTweet
          ? `推文 @${parsedTweet.author.screenName} (${parsedTweet.id})`
          : `Tweet ${targetUrl.split("/").pop() || targetUrl}`;
        return [await createJob("tweet_link", targetUrl, title)];
      }

      if (currentBatchItems.length === 0) {
        throw new Error("请至少输入一个有效目标");
      }

      if (isSchedule) {
        const payload: ArchiveScheduleRequest = {
          name:
            scheduleName.trim() ||
            `自动归档 (${currentBatchItems.length} 个目标)`,
          enabled: true,
          intervalMinutes,
          items: currentBatchItems,
        };
        await createArchiveSchedule(payload);
        return [];
      }

      return await createJobsBatch({ items: currentBatchItems });
    },
    onSuccess: (jobs) => {
      void invalidateWorkbenchQueries(queryClient);
      if (isSchedule) {
        toast.success({
          message: "定时归档计划已保存",
          description: `执行频率: 每 ${intervalMinutes} 分钟`,
        });
      } else {
        toast.success({
          message: "任务创建成功",
          description: `已成功入库 ${jobs.length} 个任务并开始后台下载`,
        });
      }
      handleClose();
    },
    onError: notifyError("创建失败"),
  });

  const handleClose = () => {
    setTweetUrl("");
    setBatchInputs({});
    setParsedTweet(null);
    setIsSchedule(false);
    setScheduleName("");
    onClose();
  };

  const handleParseTweet = () => {
    const trimmed = tweetUrl.trim();
    if (!trimmed) return;
    setParsedTweet(null);
    parseMutation.mutate(trimmed);
  };

  const tabItems: TabItem[] = [
    ...batchTabs.map((tab) => ({
      key: tab.kind,
      label: (
        <span className="flex items-center gap-1.5">
          {tab.icon}
          <span>{tab.label}</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex items-center justify-between text-xs text-fg-muted">
            <span>{tab.hint}</span>
            <span className="font-mono font-medium text-brand-600 dark:text-brand-400">
              已识别: {currentBatchItems.length} 个
            </span>
          </div>
          <Textarea
            rows={6}
            value={batchInputs[tab.kind] ?? ""}
            onChange={(e) =>
              setBatchInputs((current) => ({ ...current, [tab.kind]: e.target.value }))
            }
            placeholder={tab.placeholder}
            className="font-mono text-xs"
          />
        </div>
      ),
    })),
    {
      key: "tweet_link",
      label: (
        <span className="flex items-center gap-1.5">
          <LinkIcon className="size-4" />
          <span>单条推文</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex gap-2">
            <Input
              value={tweetUrl}
              onChange={(e) => {
                setTweetUrl(e.target.value);
                setParsedTweet(null);
              }}
              onPressEnter={handleParseTweet}
              placeholder="https://x.com/username/status/1234567890"
            />
            <Button
              variant="primary"
              disabled={!tweetUrl.trim()}
              loading={parseMutation.isPending}
              onClick={handleParseTweet}
              className="shrink-0"
            >
              解析推文
            </Button>
          </div>

          {parsedTweet && (
            <div className="rounded-card border border-line bg-surface-muted/60 p-3.5 space-y-2 text-xs">
              <div className="flex items-center justify-between">
                <span className="font-semibold text-fg">
                  @{parsedTweet.author.screenName} ({parsedTweet.author.name})
                </span>
                <span className="font-medium text-success">
                  {parsedTweet.media.length} 个可用媒体
                </span>
              </div>
              <p className="line-clamp-2 text-fg-body">
                {parsedTweet.text || "无正文"}
              </p>
              <div className="flex items-center gap-2 text-[11px] text-fg-subtle">
                <span>推文 ID: {parsedTweet.id}</span>
                <span>·</span>
                <span>发布于: {parsedTweet.createdAt || "未知"}</span>
              </div>
            </div>
          )}
        </div>
      ),
    },
  ];

  const canSubmit =
    activeTab === "tweet_link"
      ? Boolean(tweetUrl.trim())
      : currentBatchItems.length > 0;

  return (
    <Modal
      open={open}
      onClose={handleClose}
      className="w-[min(92vw,44rem)]"
      title={
        <div className="flex items-center gap-3">
          <div className="flex size-8 items-center justify-center rounded-control bg-brand-50 text-brand-600 font-bold dark:bg-brand-500/12">
            <Plus className="size-4" />
          </div>
          <div>
            <div className="text-base font-semibold text-fg">
              新建下载任务 / 归档计划
            </div>
            <div className="text-xs text-fg-muted font-normal">
              支持单条推文、批量用户时间线、列表与关注关系媒体高速入库
            </div>
          </div>
        </div>
      }
      footer={
        <div className="flex w-full items-center justify-between">
          <span className="text-xs text-fg-subtle">
            {activeTab === "tweet_link"
              ? "解析后点击立即下载入库"
              : isSchedule
              ? `将保存为包含 ${currentBatchItems.length} 个目标的定时计划`
              : `将同时创建 ${currentBatchItems.length} 个后台下载任务`}
          </span>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={handleClose}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!canSubmit}
              loading={createJobsMutation.isPending}
              onClick={() => createJobsMutation.mutate()}
              icon={<Download className="size-4" />}
            >
              {isSchedule
                ? "保存定时计划"
                : activeTab === "tweet_link"
                ? "立即下载"
                : `批量下载 (${currentBatchItems.length})`}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-4 py-1">
        <Tabs
          value={activeTab}
          onChange={(key) => {
            setActiveTab(key);
            setParsedTweet(null);
          }}
          items={tabItems}
        />

        {/* 存为定时归档计划选项 (单条推文除外) */}
        {activeTab !== "tweet_link" && (
          <div className="space-y-3 rounded-card border border-line bg-surface-muted/50 p-3.5">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Clock className="size-4 text-brand-500" />
                <span className="text-xs font-semibold text-fg">
                  是否保存为自动归档计划（定期抓取）？
                </span>
              </div>
              <Checkbox
                checked={isSchedule}
                onChange={(checked) => setIsSchedule(checked)}
              />
            </div>

            {isSchedule && (
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-3 border-t border-line text-xs">
                <div>
                  <div className="mb-1 text-fg-muted font-medium">计划名称</div>
                  <Input
                    value={scheduleName}
                    onChange={(e) => setScheduleName(e.target.value)}
                    placeholder="例如：重点博主媒体日常归档"
                    size="sm"
                  />
                </div>
                <div>
                  <div className="mb-1 text-fg-muted font-medium">调度频率 (分钟)</div>
                  <NumberInput
                    min={5}
                    max={43200}
                    value={intervalMinutes}
                    onChange={(val) => setIntervalMinutes(val ?? 360)}
                    size="sm"
                    addonAfter="分钟"
                  />
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}
