import { t, useTranslation } from "../../lib/i18n";
import {
  Bookmark,
  ChevronDown,
  ChevronUp,
  Clock,
  Download,
  Eraser,
  Filter,
  Heart,
  Link as LinkIcon,
  List as ListIcon,
  MessageSquareText,
  Plus,
  Search,
  User,
  UserPlus,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useId, useMemo, useRef, useState } from "react";
import {
  createArchiveSchedule,
  createJob,
  createJobsBatch,
  parseTweetLink,
  type ArchiveFilter,
  type ArchiveScheduleRequest,
  type JobKind,
  type JobRequest,
  type TweetData,
  type ArchiveMode,
} from "../../lib/api";
import { detectQuickInputTab } from "../../lib/quickInput";
import { formatIntervalMinutes, notifyError } from "../common/CommonUI";
import { ArchiveFilterFields, cleanArchiveFilter, countArchiveFilterRules } from "../common/ArchiveFilterFields";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";
import Button from "@douyinfe/semi-ui/lib/es/button";
import RadioGroup from "@douyinfe/semi-ui/lib/es/radio/radioGroup";
import { Checkbox, Tabs, type TabItem, Field, Input, NumberInput, Select, Textarea } from "../semi/controls";
import { Modal } from "../semi/overlays";
import { Tag } from "../semi/data";
import { toast } from "../semi/toast";

export { detectQuickInputTab };

export interface CreateJobModalProps {
  open: boolean;
  onClose: () => void;
  initialInput?: string;
  initialKind?: JobKind | "schedule";
}

const tabKeys: string[] = ["user", "tweet_link", "list", "following", "thread", "bookmarks", "likes", "search"];
const nonSchedulableTabs = new Set(["tweet_link", "thread"]);

type BatchKind = Extract<JobKind, "user" | "list" | "following">;

function getBatchTabs(): {
  kind: BatchKind;
  label: string;
  icon: React.ReactNode;
  hint: string;
  placeholder: string;
}[] {
  return [
    {
      kind: "user",
      label: t("用户归档"),
      icon: <User className="size-4" />,
      hint: t("支持用户名、@screen_name 或数字 ID，每行一个"),
      placeholder: "elonmusk\n@sama\n44196397\nhttps://x.com/OpenAI",
    },
    {
      kind: "list",
      label: t("列表归档"),
      icon: <ListIcon className="size-4" />,
      hint: t("输入 X 列表 ID 或完整列表 URL，自动获取列表成员推文媒体"),
      placeholder: "1492019283\nhttps://x.com/i/lists/1647289190",
    },
    {
      kind: "following",
      label: t("关注关系归档"),
      icon: <UserPlus className="size-4" />,
      hint: t("输入目标账号，自动获取其关注的所有账号并进行媒体归档"),
      placeholder: "elonmusk\n@OpenAI",
    },
  ];
}

// 可选协议 / www. / mobile.，x.com 或 twitter.com，支持 /i/user/<id>、@handle、纯 handle 与 1-20 位数字 ID。
const userIdLinkPattern =
  /^(?:https?:\/\/)?(?:(?:www|mobile)\.)?(?:x|twitter)\.com\/i\/user\/(\d{1,20})(?:[/?#]|$)/i;
const profileLinkPattern =
  /^(?:https?:\/\/)?(?:(?:www|mobile)\.)?(?:x|twitter)\.com\/@?([A-Za-z0-9_]{1,15})(?:[/?#]|$)/i;
const plainUserIdPattern = /^(\d{1,20})$/;
const plainHandlePattern = /^@?([A-Za-z0-9_]{1,15})$/;
const reservedProfilePaths = new Set([
  "i",
  "home",
  "explore",
  "search",
  "notifications",
  "messages",
  "settings",
  "lists",
  "hashtag",
  "compose",
  "login",
  "logout",
  "signup",
  "tos",
  "privacy",
]);
const xLinkPattern = /^(?:https?:\/\/)?(?:(?:www|mobile)\.)?(?:x|twitter)\.com(?:[/?#]|$)/i;
const statusLinkPattern = /\/status(?:es)?\/(\d{1,20})(?:[/?#]|$)/i;

function normalizeTweetURL(input: string) {
  const trimmed = input.trim();
  return xLinkPattern.test(trimmed) && !/^https?:\/\//i.test(trimmed) ? `https://${trimmed}` : trimmed;
}

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// 从主页链接、/i/user/<id>、@handle、纯 handle 或最长 20 位数字 ID 中取出用户标识；无法识别时返回 null。
export function parseScreenName(input: string): string | null {
  const trimmed = input.trim();
  const userIdLink = trimmed.match(userIdLinkPattern);
  if (userIdLink) return userIdLink[1];
  const profileLink = trimmed.match(profileLinkPattern);
  if (profileLink) {
    return reservedProfilePaths.has(profileLink[1].toLowerCase()) ? null : profileLink[1];
  }
  const plainUserId = trimmed.match(plainUserIdPattern);
  if (plainUserId) return plainUserId[1];
  const plainHandle = trimmed.match(plainHandlePattern);
  return plainHandle ? plainHandle[1] : null;
}

export interface BatchInputLine {
  lineNumber: number;
  raw: string;
  normalized: string;
  status: "valid" | "pending" | "duplicate" | "invalid";
  duplicateOf?: number;
}

export interface BatchInputSummary {
  items: JobRequest[];
  recognized: number;
  pendingValidation: number;
  duplicates: number;
  invalid: number;
  lines: BatchInputLine[];
}

export function parseBatchInput(raw: string, kind: JobKind): BatchInputSummary {
  const seen = new Set<string>();
  const firstLineByValue = new Map<string, number>();
  const items: JobRequest[] = [];
  const lines: BatchInputLine[] = [];
  let recognized = 0;
  let pendingValidation = 0;
  let duplicates = 0;
  let invalid = 0;
  for (const [lineIndex, rawLine] of raw.split(/\r?\n/).entries()) {
    let line = rawLine.trim();
    if (!line) continue;

    // 清洗常见的输入前缀
    let validFormat = false;
    if (kind === "user" || kind === "following") {
      const parsed = parseScreenName(line);
      validFormat = parsed !== null;
      if (parsed) line = parsed;
    } else if (kind === "list") {
      const match = xLinkPattern.test(line) ? line.match(/\/lists\/(\d+)(?:[/?#]|$)/i) : line.match(/^(\d+)$/);
      validFormat = match !== null;
      if (match) line = match[1];
    } else if (kind === "tweet_link") {
      const normalized = normalizeTweetURL(line);
      if ((xLinkPattern.test(normalized) && statusLinkPattern.test(normalized)) || plainUserIdPattern.test(normalized)) {
        validFormat = true;
        line = plainUserIdPattern.test(normalized) ? `https://x.com/i/status/${normalized}` : normalized;
      }
    }

    if (!line) continue;
    if (!validFormat) {
      invalid += 1;
      lines.push({ lineNumber: lineIndex + 1, raw: rawLine, normalized: line, status: "invalid" });
      continue;
    }
    const key = line.toLowerCase();
    if (seen.has(key)) {
      duplicates += 1;
      lines.push({ lineNumber: lineIndex + 1, raw: rawLine, normalized: line, status: "duplicate", duplicateOf: firstLineByValue.get(key) });
      continue;
    }
    recognized += 1;
    const pending = kind === "list" || /^\d+$/.test(line);
    if (pending) pendingValidation += 1;
    lines.push({ lineNumber: lineIndex + 1, raw: rawLine, normalized: line, status: pending ? "pending" : "valid" });
    firstLineByValue.set(key, lineIndex + 1);
    seen.add(key);
    items.push({ kind, input: line });
  }

  return { items, recognized, pendingValidation, duplicates, invalid, lines };
}

type BatchTabMeta = ReturnType<typeof getBatchTabs>[number];

function BatchTabContent({ tab, summary, value, onValueChange, onRemoveLine, onCleanLines }: {
  tab: BatchTabMeta;
  summary: ReturnType<typeof parseBatchInput>;
  value: string;
  onValueChange: (value: string) => void;
  onRemoveLine: (kind: BatchKind, lineNumber: number) => void;
  onCleanLines: (kind: BatchKind) => void;
}) {
  useTranslation();
  const hasProblems = summary.duplicates > 0 || summary.invalid > 0;
  return (
    <div className="space-y-3 pt-1">
      <div className="flex items-center justify-between gap-2 text-xs text-fg-muted">
        <span>{tab.hint}</span>
        <span className="shrink-0 whitespace-nowrap font-semibold text-brand-600 tabular-nums">
          {t("待校验目标")} {summary.items.length} {t(" 个")}</span>
      </div>
      {(summary.recognized > 0 || summary.pendingValidation > 0 || hasProblems) && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-fg-subtle tabular-nums" aria-live="polite">
            {summary.recognized > 0 && <span>{t("格式有效")} {summary.recognized}</span>}
            {summary.pendingValidation > 0 && <span>{t("待后端校验")} {summary.pendingValidation}</span>}
            {summary.duplicates > 0 && <span className="text-warning-strong">{t("重复目标")} {summary.duplicates}</span>}
            {summary.invalid > 0 && <span className="text-danger">{t("无效格式")} {summary.invalid}</span>}
          </div>
          {hasProblems && (
            <Button
              size="small"
              type="tertiary"
              icon={<Eraser className="size-3.5" />}
              onClick={() => onCleanLines(tab.kind)}
            >
              {t("清理无效与重复行")}
            </Button>
          )}
        </div>
      )}
      <Textarea
        rows={5}
        aria-label={tab.label}
        value={value}
        onChange={(e) => onValueChange(e.target.value)}
        placeholder={tab.placeholder}
        className="font-mono text-xs"
      />
      {summary.lines.length > 0 && (
        <div className="create-job-validation-list space-y-1.5" aria-label={t("逐行校验结果")}>
          {summary.lines.map(line => (
            <div key={`${tab.kind}-${line.lineNumber}`} className="flex min-w-0 items-center gap-2 text-xs">
              <span className="w-12 shrink-0 text-fg-subtle tabular-nums">{t("第 {{value0}} 行", { value0: line.lineNumber })}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-fg-muted" title={line.raw}>{line.normalized || line.raw}</span>
              {line.status === "valid" && <Tag tone="success" size="sm">{t("格式有效")}</Tag>}
              {line.status === "pending" && <Tag tone="warning" size="sm">{t("待后端校验")}</Tag>}
              {line.status === "invalid" && <Tag tone="danger" size="sm">{t("无效格式，已忽略")}</Tag>}
              {line.status === "duplicate" && <><Tag tone="danger" size="sm">{t("与第 {{value0}} 行重复，已忽略", { value0: line.duplicateOf })}</Tag><Button type="tertiary" size="small" onClick={() => onRemoveLine(tab.kind, line.lineNumber)}>{t("移除重复行")}</Button></>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function TweetLinkTabContent({
  value,
  batchMode,
  batchSummary,
  onToggleBatchMode,
  onChange,
  onParse,
  parsePending,
  parsedTweet,
}: {
  value: string;
  batchMode: boolean;
  batchSummary: BatchInputSummary;
  onToggleBatchMode: () => void;
  onChange: (value: string) => void;
  onParse: () => void;
  parsePending: boolean;
  parsedTweet: TweetData | null;
}) {
  useTranslation();
  return (
    <div className="space-y-3 pt-1">
      <div className="flex items-center justify-between gap-2 text-xs text-fg-muted">
        <span>{batchMode ? t("每行粘贴一条 X 推文链接或推文 ID，批量创建下载任务") : t("粘贴单条推文链接，可先解析预览或直接下载")}</span>
        <Button size="small" type="tertiary" onClick={onToggleBatchMode}>
          {batchMode ? t("切换单条解析") : t("批量多条链接")}
        </Button>
      </div>
      {batchMode ? (
        <div className="space-y-2">
          <Textarea
            rows={5}
            aria-label={t("批量推文链接")}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={"https://x.com/username/status/1234567890\nhttps://x.com/openai/status/9876543210"}
            className="font-mono text-xs"
          />
          {batchSummary.lines.length > 0 && (
            <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-fg-subtle tabular-nums" aria-live="polite">
              <span className="font-semibold text-brand-600">{t("有效推文链接")} {batchSummary.items.length}</span>
              {batchSummary.duplicates > 0 && <span className="text-warning-strong">{t("重复目标")} {batchSummary.duplicates}</span>}
              {batchSummary.invalid > 0 && <span className="text-danger">{t("无效格式")} {batchSummary.invalid}</span>}
            </div>
          )}
        </div>
      ) : (
        <div className="flex gap-2">
          <Input
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onPaste={(event) => {
              const pasted = event.clipboardData?.getData("text") ?? "";
              if (pasted.includes("\n") && pasted.trim().split(/\r?\n/).filter(Boolean).length > 1) {
                event.preventDefault();
                onChange(pasted);
                onToggleBatchMode();
              }
            }}
            onPressEnter={onParse}
            placeholder="https://x.com/username/status/1234567890"
          />
          <Button
            theme="solid"
            disabled={!value.trim() || parsePending}
            loading={parsePending}
            aria-busy={parsePending}
            onClick={onParse}
            className="shrink-0"
          >
            {t("解析推文")}</Button>
        </div>
      )}

      {!batchMode && parsedTweet && (
        <div className="rounded-card border border-line bg-surface-muted/60 p-3.5 space-y-2 text-xs">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="min-w-0 break-all font-semibold text-fg">
              @{parsedTweet.author.screenName} ({parsedTweet.author.name})
            </span>
            <span className="font-semibold text-success tabular-nums">
              {parsedTweet.media.length} {t(" 个可用媒体")}</span>
          </div>
          <p className="line-clamp-2 text-fg-body">
            {parsedTweet.text || t("无正文")}
          </p>
          <div className="flex flex-wrap items-center gap-2 text-xs text-fg-subtle">
            <span>{t("推文 ID: ")}{parsedTweet.id}</span>
            <span>·</span>
            <span>{t("发布于: ")}{parsedTweet.createdAt || t("未知")}</span>
          </div>
        </div>
      )}
    </div>
  );
}

export function CreateJobModal({
  open,
  onClose,
  initialInput = "",
  initialKind = "user",
}: CreateJobModalProps) {
  useTranslation();
  const queryClient = useQueryClient();
  const customIntervalId = useId();
  const batchSizeId = useId();
  const cronInputId = useId();
  const tzInputId = useId();
  const [activeTab, setActiveTab] = useState<string>("user");
  const [archiveMode, setArchiveMode] = useState<ArchiveMode>("both");
  const [backfill, setBackfill] = useState(false);
  const [batchSize, setBatchSize] = useState(0);
  const [limitTargets, setLimitTargets] = useState(false);

  const [tweetUrl, setTweetUrl] = useState("");
  const [tweetBatchMode, setTweetBatchMode] = useState(false);
  const [batchInputs, setBatchInputs] = useState<Partial<Record<BatchKind, string>>>({});
  const [threadUrl, setThreadUrl] = useState("");
  const [threadIncludeReplies, setThreadIncludeReplies] = useState(false);
  const [bookmarkFolderId, setBookmarkFolderId] = useState("");
  const [likesTarget, setLikesTarget] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const [searchProduct, setSearchProduct] = useState<"latest" | "top" | "media">("latest");

  const [filterOpen, setFilterOpen] = useState(false);
  const [archiveFilter, setArchiveFilter] = useState<ArchiveFilter | null>(null);

  const [parsedTweet, setParsedTweet] = useState<TweetData | null>(null);
  const parseSequence = useRef(0);
  const createSequence = useRef(0);

  const [isSchedule, setIsSchedule] = useState(false);
  const [scheduleName, setScheduleName] = useState("");
  const [scheduleTriggerType, setScheduleTriggerType] = useState<"interval" | "cron">("interval");
  const [intervalMinutes, setIntervalMinutes] = useState<number | null>(360);
  const [customFrequency, setCustomFrequency] = useState(false);
  const [cronExpr, setCronExpr] = useState("0 3 * * *");
  const [timezone, setTimezone] = useState(() => browserTimeZone());
  const openRef = useRef(open);
  openRef.current = open;

  const resetDraftAndOptions = () => {
    setTweetUrl("");
    setTweetBatchMode(false);
    setBatchInputs({});
    setThreadUrl("");
    setThreadIncludeReplies(false);
    setBookmarkFolderId("");
    setLikesTarget("");
    setSearchQuery("");
    setSearchProduct("latest");
    setArchiveFilter(null);
    setFilterOpen(false);
    setScheduleName("");
    setIsSchedule(false);
    setScheduleTriggerType("interval");
    setArchiveMode("both");
    setBackfill(false);
    setBatchSize(0);
    setLimitTargets(false);
    setIntervalMinutes(360);
    setCustomFrequency(false);
    setCronExpr("0 3 * * *");
    setTimezone(browserTimeZone());
  };

  useEffect(() => {
    parseSequence.current += 1;
    createSequence.current += 1;
    setParsedTweet(null);
    if (!open) return;
    if (initialInput) {
      const quickTab = detectQuickInputTab(initialInput);
      setActiveTab(quickTab);
      if (quickTab === "tweet_link") {
        setTweetBatchMode(false);
        setTweetUrl(normalizeTweetURL(initialInput));
      } else {
        setBatchInputs({ [quickTab]: initialInput });
      }
    } else if (!Object.values(batchInputs).some(value => value?.trim()) && !tweetUrl.trim()) {
      setActiveTab(tabKeys.includes(initialKind) ? initialKind : "user");
    }
    if (initialKind === "schedule") {
      setIsSchedule(true);
      setActiveTab(current => nonSchedulableTabs.has(current) ? "user" : current);
    } else {
      setIsSchedule(false);
    }
  }, [open, initialInput, initialKind]);

  // 单条推文与长串不支持定时计划：切到该标签时勾选状态保留（切回可恢复），但不参与任何展示与提交。
  const scheduleMode = isSchedule && !nonSchedulableTabs.has(activeTab);
  const effectiveBackfill = !nonSchedulableTabs.has(activeTab) && archiveMode !== "media" && backfill;

  const currentBatchSummary = useMemo(() => {
    if (activeTab === "user" || activeTab === "list" || activeTab === "following") {
      const raw = batchInputs[activeTab as BatchKind] ?? "";
      return parseBatchInput(raw, activeTab as JobKind);
    }
    if (activeTab === "tweet_link" && tweetBatchMode) {
      return parseBatchInput(tweetUrl, "tweet_link");
    }
    return { items: [], recognized: 0, pendingValidation: 0, duplicates: 0, invalid: 0, lines: [] };
  }, [activeTab, batchInputs, tweetBatchMode, tweetUrl]);

  const resolvedTabItems = useMemo<JobRequest[]>(() => {
    if (activeTab === "user" || activeTab === "list" || activeTab === "following") {
      return currentBatchSummary.items;
    }
    if (activeTab === "bookmarks") {
      const folder = bookmarkFolderId.trim();
      const input = folder ? (folder.startsWith("folder:") ? folder : `folder:${folder}`) : "all";
      return [{ kind: "bookmarks", input, title: folder ? t("书签目录 {{value0}}", { value0: folder }) : t("全部书签") }];
    }
    if (activeTab === "likes") {
      const target = likesTarget.trim();
      const input = target || "self";
      return [{ kind: "likes", input, title: target ? t("点赞 @{{value0}}", { value0: target.replace(/^@/, "") }) : t("我的点赞") }];
    }
    if (activeTab === "search") {
      const q = searchQuery.trim();
      if (!q) return [];
      const input = `${searchProduct}:${q}`;
      return [{ kind: "search", input, title: t("搜索: {{value0}}", { value0: q }) }];
    }
    if (activeTab === "thread") {
      const target = normalizeTweetURL(threadUrl);
      if (!target) return [];
      const input = threadIncludeReplies ? `replies:${target}` : target;
      return [{ kind: "thread", input, title: t("长串归档") }];
    }
    return [];
  }, [activeTab, currentBatchSummary.items, bookmarkFolderId, likesTarget, searchQuery, searchProduct, threadUrl, threadIncludeReplies]);

  const activeFilterRule = useMemo(() => cleanArchiveFilter(archiveFilter), [archiveFilter]);
  const activeFilterCount = useMemo(() => countArchiveFilterRules(archiveFilter), [archiveFilter]);

  function removeBatchLine(kind: BatchKind, lineNumber: number) {
    setBatchInputs(current => {
      const lines = (current[kind] ?? "").split(/\r?\n/);
      if (lineNumber < 1 || lineNumber > lines.length) return current;
      lines.splice(lineNumber - 1, 1);
      return { ...current, [kind]: lines.join("\n") };
    });
  }

  function cleanBatchLines(kind: BatchKind) {
    const raw = batchInputs[kind] ?? "";
    const parsed = parseBatchInput(raw, kind);
    const badLineNumbers = new Set(
      parsed.lines
        .filter(line => line.status === "invalid" || line.status === "duplicate")
        .map(line => line.lineNumber),
    );
    if (badLineNumbers.size === 0) return;
    const kept = raw
      .split(/\r?\n/)
      .filter((_line, index) => !badLineNumbers.has(index + 1));
    setBatchInputs(current => ({ ...current, [kind]: kept.join("\n") }));
    toast.success({
      message: t("已清理 {{value0}} 行无效或重复目标", { value0: badLineNumbers.size }),
    });
  }

  const parseMutation = useMutation({
    mutationFn: ({ url }: { url: string; sequence: number }) => parseTweetLink(url),
    onSuccess: (data, request) => {
      if (request.sequence !== parseSequence.current) return;
      setParsedTweet(data);
      toast.success({
        message: t("推文解析成功"),
        description: t("包含 {{value0}} 个媒体，作者 @{{value1}}", { value0: data.media.length, value1: data.author.screenName }),
      });
    },
    onError: (error, request) => {
      if (request.sequence === parseSequence.current) notifyError(t("解析失败"))(error);
    },
  });

  const createJobsMutation = useMutation({
    onMutate: () => ({
      sequence: createSequence.current,
      scheduleMode,
      intervalMinutes: intervalMinutes ?? 0,
      cronLabel: scheduleTriggerType === "cron" ? cronExpr.trim() : "",
    }),
    mutationFn: async () => {
      const filterPayload = activeFilterRule ? { filter: activeFilterRule } : {};

      if (activeTab === "tweet_link") {
        if (tweetBatchMode) {
          if (currentBatchSummary.items.length === 0) {
            throw new Error(t("请至少输入一条有效推文链接"));
          }
          return await createJobsBatch({
            items: currentBatchSummary.items.map(item => ({
              ...item,
              archiveMode,
              backfill: false,
              ...filterPayload,
            })),
          });
        }
        const targetUrl = normalizeTweetURL(tweetUrl);
        if (!targetUrl) throw new Error(t("请输入推文链接"));
        const title = parsedTweet
          ? t("推文 @{{value0}} ({{value1}})", { value0: parsedTweet.author.screenName, value1: parsedTweet.id })
          : `Tweet ${targetUrl.split("/").pop() || targetUrl}`;
        return [await createJob("tweet_link", targetUrl, title, { archiveMode, backfill: effectiveBackfill, ...filterPayload })];
      }

      if (activeTab === "thread") {
        const item = resolvedTabItems[0];
        if (!item) throw new Error(t("请输入推文链接或推文 ID"));
        return [await createJob("thread", item.input, item.title ?? "", { archiveMode, backfill: false, ...filterPayload })];
      }

      if (resolvedTabItems.length === 0) {
        throw new Error(t("请至少输入一个有效目标"));
      }

      if (scheduleMode) {
        if (scheduleTriggerType === "cron") {
          if (!cronExpr.trim()) {
            throw new Error(t("请输入 Cron 表达式"));
          }
        } else if (intervalMinutes === null || !Number.isInteger(intervalMinutes) || intervalMinutes < 5 || intervalMinutes > 43200) {
          throw new Error(t("调度频率 (分钟)"));
        }
        const payload: ArchiveScheduleRequest = {
          name:
            scheduleName.trim() ||
            t("自动归档 ({{value0}} 个目标)", { value0: resolvedTabItems.length }),
          enabled: true,
          intervalMinutes: scheduleTriggerType === "cron" ? (intervalMinutes ?? 360) : intervalMinutes!,
          ...(scheduleTriggerType === "cron"
            ? { cronExpr: cronExpr.trim(), timezone: timezone.trim() || "UTC" }
            : {}),
          items: resolvedTabItems.map(item => ({
            ...item,
            archiveMode,
            backfill: effectiveBackfill,
            batchSize: archiveMode === "media" || !limitTargets ? 0 : batchSize,
            ...filterPayload,
          })),
        };
        await createArchiveSchedule(payload);
        return [];
      }

      return await createJobsBatch({
        items: resolvedTabItems.map(item => ({
          ...item,
          archiveMode,
          backfill: effectiveBackfill,
          ...filterPayload,
        })),
      });
    },
    onSuccess: (jobs, _variables, request) => {
      void invalidateWorkbenchQueries(queryClient);
      const isCurrent = request?.sequence === createSequence.current;
      // 弹窗在提交期间被关闭且未重新打开时，仍然提示提交结果并重置草稿，避免静默丢失反馈。
      if (isCurrent || !openRef.current) {
        if (request?.scheduleMode) {
          toast.success({
            message: t("定时归档计划已保存"),
            description: request.cronLabel ? `Cron: ${request.cronLabel}` : formatIntervalMinutes(request.intervalMinutes),
          });
        } else {
          toast.success({
            message: t("任务创建成功"),
            description: t("已成功入库 {{value0}} 个任务并开始后台下载", { value0: jobs.length }),
          });
        }
        resetDraftAndOptions();
        if (isCurrent) {
          handleClose();
        }
      }
    },
    onError: (error, _variables, request) => {
      if (request?.sequence === createSequence.current || !openRef.current) {
        notifyError(t("创建失败"))(error);
      }
    },
  });

  const handleClose = () => {
    parseSequence.current += 1;
    createSequence.current += 1;
    setParsedTweet(null);
    setIsSchedule(false);
    onClose();
  };

  const handleParseTweet = () => {
    if (parseMutation.isPending) return;
    const trimmed = normalizeTweetURL(tweetUrl);
    if (!trimmed) return;
    setParsedTweet(null);
    parseSequence.current += 1;
    parseMutation.mutate({ url: trimmed, sequence: parseSequence.current });
  };

  // 关闭时只保留标签，内容组件不构建也不挂载；Semi Tabs 自身只挂载当前 tab 的内容。
  const tabItems: TabItem[] = [
    ...getBatchTabs().map((tab) => ({
      key: tab.kind,
      label: (
        <span className="flex items-center gap-1.5">
          {tab.icon}
          <span>{tab.label}</span>
        </span>
      ),
      children: open ? (
        <BatchTabContent
          tab={tab}
          summary={currentBatchSummary}
          value={batchInputs[tab.kind] ?? ""}
          onValueChange={value => setBatchInputs(current => ({ ...current, [tab.kind]: value }))}
          onRemoveLine={removeBatchLine}
          onCleanLines={cleanBatchLines}
        />
      ) : null,
    })),
    {
      key: "tweet_link",
      label: (
        <span className="flex items-center gap-1.5">
          <LinkIcon className="size-4" />
          <span>{t("单条推文")}</span>
        </span>
      ),
      children: open ? (
        <TweetLinkTabContent
          value={tweetUrl}
          batchMode={tweetBatchMode}
          batchSummary={currentBatchSummary}
          onToggleBatchMode={() => {
            setParsedTweet(null);
            setTweetBatchMode((mode) => !mode);
          }}
          onChange={value => { parseSequence.current += 1; setTweetUrl(value); setParsedTweet(null); }}
          onParse={handleParseTweet}
          parsePending={parseMutation.isPending}
          parsedTweet={parsedTweet}
        />
      ) : null,
    },
    {
      key: "thread",
      label: (
        <span className="flex items-center gap-1.5">
          <MessageSquareText className="size-4" />
          <span>{t("长串归档")}</span>
        </span>
      ),
      children: open ? (
        <div className="space-y-3 pt-1">
          <p className="text-xs text-fg-muted">{t("输入长串中任意一条推文的链接或 ID，自动抓取整条作者对话线程")}</p>
          <Input
            aria-label={t("长串推文链接")}
            value={threadUrl}
            onChange={(e) => setThreadUrl(e.target.value)}
            placeholder="https://x.com/username/status/1234567890"
          />
          <Checkbox
            checked={threadIncludeReplies}
            onChange={setThreadIncludeReplies}
            label={t("同时抓取他人回复（默认仅抓取作者自连长串）")}
          />
        </div>
      ) : null,
    },
    {
      key: "bookmarks",
      label: (
        <span className="flex items-center gap-1.5">
          <Bookmark className="size-4" />
          <span>{t("书签归档")}</span>
        </span>
      ),
      children: open ? (
        <div className="space-y-3 pt-1">
          <p className="text-xs text-fg-muted">{t("归档当前已登录 X 账号的书签；留空文件夹 ID 则同步全部书签")}</p>
          <Field label={t("书签文件夹 ID (可选)")}>
            <Input
              aria-label={t("书签文件夹 ID (可选)")}
              value={bookmarkFolderId}
              onChange={(e) => setBookmarkFolderId(e.target.value)}
              placeholder={t("留空归档全部书签，或填写文件夹数字 ID")}
            />
          </Field>
        </div>
      ) : null,
    },
    {
      key: "likes",
      label: (
        <span className="flex items-center gap-1.5">
          <Heart className="size-4" />
          <span>{t("点赞归档")}</span>
        </span>
      ),
      children: open ? (
        <div className="space-y-3 pt-1">
          <p className="text-xs text-fg-muted">{t("归档已点赞的推文与媒体；留空则同步当前登录 X 账号的点赞列表")}</p>
          <Field label={t("目标账号 (可选)")}>
            <Input
              aria-label={t("目标账号 (可选)")}
              value={likesTarget}
              onChange={(e) => setLikesTarget(e.target.value)}
              placeholder={t("留空归档当前账号，或填写 @username / 用户 ID")}
            />
          </Field>
        </div>
      ) : null,
    },
    {
      key: "search",
      label: (
        <span className="flex items-center gap-1.5">
          <Search className="size-4" />
          <span>{t("搜索归档")}</span>
        </span>
      ),
      children: open ? (
        <div className="space-y-3 pt-1">
          <p className="text-xs text-fg-muted">{t("支持 X 高级搜索语法，例如 #AI filter:media min_faves:100 from:OpenAI")}</p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <Field label={t("搜索关键词 / 高级语法")} className="sm:col-span-2">
              <Input
                aria-label={t("搜索关键词 / 高级语法")}
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="#artwork filter:images min_faves:50"
              />
            </Field>
            <Field label={t("搜索排序")}>
              <Select
                ariaLabel={t("搜索排序")}
                value={searchProduct}
                onChange={(v) => setSearchProduct(v as "latest" | "top" | "media")}
                options={[
                  { value: "latest", label: t("最新 (Latest)") },
                  { value: "top", label: t("热门 (Top)") },
                  { value: "media", label: t("媒体 (Media)") },
                ]}
              />
            </Field>
          </div>
        </div>
      ) : null,
    },
  ];

  const canSubmit =
    activeTab === "tweet_link"
      ? tweetBatchMode
        ? currentBatchSummary.items.length > 0
        : Boolean(tweetUrl.trim())
      : resolvedTabItems.length > 0;

  const scheduleValid =
    scheduleTriggerType === "cron"
      ? Boolean(cronExpr.trim())
      : intervalMinutes !== null &&
        Number.isInteger(intervalMinutes) &&
        intervalMinutes >= 5 &&
        intervalMinutes <= 43200;

  const submitDisabled =
    !canSubmit ||
    createJobsMutation.isPending ||
    (scheduleMode &&
      (!scheduleValid ||
        (archiveMode !== "media" && limitTargets && (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 1000))));

  const handleModalKeyDown = (event: React.KeyboardEvent) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && !event.nativeEvent.isComposing) {
      if (!submitDisabled) {
        event.preventDefault();
        createJobsMutation.mutate();
      }
    }
  };

  const targetCount = activeTab === "tweet_link" && tweetBatchMode
    ? currentBatchSummary.items.length
    : resolvedTabItems.length;

  return (
    <Modal
      open={open}
      onClose={handleClose}
      width="min(92vw, 44rem)"
      className="create-job-modal"
      title={
        <div className="flex items-center gap-3">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-control bg-brand-50 text-brand-600">
            <Plus className="size-4" />
          </div>
          <div className="min-w-0">
            {/* 标题字号交给 Semi 的模态标题（18/24），与其它弹窗同一层级。 */}
            <div>{scheduleMode ? t("新建归档计划") : t("新建下载任务")}</div>
            <div className="hidden text-xs text-fg-muted sm:block">
              {t("目标与归档设置")}</div>
          </div>
        </div>
      }
      footer={
        <div className="flex w-full flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <span className="text-xs text-fg-subtle">
            {activeTab === "tweet_link" && !tweetBatchMode
              ? t("解析后点击立即下载入库")
              : activeTab === "thread"
              ? t("将抓取并归档整条长串对话")
              : scheduleMode
              ? t("将保存为包含 {{value0}} 个目标的定时计划", { value0: targetCount })
              : t("将同时创建 {{value0}} 个后台下载任务", { value0: targetCount })}
          </span>
          <div className="flex items-center justify-end gap-2">
            <Button type="tertiary" onClick={handleClose}>
              {t("取消")}</Button>
            <Button
              theme="solid"
              disabled={submitDisabled}
              loading={createJobsMutation.isPending}
              aria-busy={createJobsMutation.isPending}
              onClick={() => createJobsMutation.mutate()}
              icon={<Download className="size-4" />}
            >
              {scheduleMode
                ? t("保存定时计划")
                : (activeTab === "tweet_link" && !tweetBatchMode) || activeTab === "thread"
                ? t("立即下载")
                : t("批量下载 ({{value0}})", { value0: targetCount })}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-4 py-1" onKeyDown={handleModalKeyDown}>
        <div className="create-job-options-bar">
          {!nonSchedulableTabs.has(activeTab) && (
            <Field label={t("执行方式")}>
              <RadioGroup
                aria-label={t("执行方式")}
                type="button"
                value={isSchedule ? "schedule" : "once"}
                onChange={event => setIsSchedule(event.target.value === "schedule")}
                options={[{ value: "once", label: t("立即执行") }, { value: "schedule", label: t("定时计划") }]}
              />
            </Field>
          )}
          <Field label={t("archive.mode")}>
            <Select
              value={archiveMode}
              onChange={value => setArchiveMode(value as ArchiveMode)}
              ariaLabel={t("archive.mode")}
              options={[
                { value: "both", label: t("archive.both") },
                { value: "tweets", label: t("archive.textOnly") },
                { value: "media", label: t("archive.mediaOnly") },
              ]}
            />
          </Field>
        </div>
        {!nonSchedulableTabs.has(activeTab) && archiveMode !== "media" && (
          <div>
            <Checkbox checked={backfill} onChange={setBackfill} label={t("archive.backfill")} />
          </div>
        )}
        <Tabs
          value={activeTab}
          onChange={(key) => {
            setActiveTab(key);
            setParsedTweet(null);
          }}
          items={tabItems}
        />

        {/* 可折叠归档过滤规则 */}
        <div className="rounded-card border border-line bg-surface-muted/30 p-3">
          <div className="flex items-center justify-between gap-2">
            <Button
              size="small"
              theme="borderless"
              type="tertiary"
              icon={<Filter className="size-3.5" />}
              aria-expanded={filterOpen}
              onClick={() => setFilterOpen((openState) => !openState)}
            >
              <span className="flex items-center gap-1.5">
                <span>{t("归档过滤规则")}</span>
                {activeFilterCount > 0 && <Tag tone="brand" size="sm">{activeFilterCount}</Tag>}
                {filterOpen ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
              </span>
            </Button>
            {activeFilterCount > 0 && (
              <Button size="small" theme="borderless" type="tertiary" onClick={() => setArchiveFilter(null)}>
                {t("重置过滤")}
              </Button>
            )}
          </div>
          {filterOpen && (
            <div className="mt-3 border-t border-line pt-3">
              <ArchiveFilterFields value={archiveFilter} onChange={setArchiveFilter} />
            </div>
          )}
        </div>

        {/* 存为定时归档计划选项 (单条推文与长串除外) */}
        {scheduleMode && (
          <div className="space-y-3 rounded-card border border-line bg-surface-muted/50 p-3.5">
            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-center gap-2">
                <Clock className="size-4 shrink-0 text-brand-500" />
                <span className="text-xs font-semibold text-fg">
                  {t("调度设置")}</span>
              </div>
              <RadioGroup
                aria-label={t("触发方式")}
                type="button"
                buttonSize="small"
                value={scheduleTriggerType}
                onChange={(e) => setScheduleTriggerType(e.target.value as "interval" | "cron")}
                options={[
                  { value: "interval", label: t("固定间隔") },
                  { value: "cron", label: "Cron" },
                ]}
              />
            </div>

            {isSchedule && (
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-3 border-t border-line text-xs">
                <Field label={t("计划名称")}>
                  <Input
                    value={scheduleName}
                    onChange={(e) => setScheduleName(e.target.value)}
                    placeholder={t("例如：重点博主媒体日常归档")}
                  />
                </Field>
                {scheduleTriggerType === "interval" ? (
                  <Field label={t("执行频率")}>
                    <Select ariaLabel={t("常用执行频率")} value={!customFrequency && intervalMinutes !== null && [30, 60, 360, 720, 1440].includes(intervalMinutes) ? String(intervalMinutes) : "custom"} onChange={value => { setCustomFrequency(value === "custom"); if (value !== "custom") setIntervalMinutes(Number(value)); }} options={[30, 60, 360, 720, 1440].map(value => ({ value: String(value), label: formatIntervalMinutes(value) })).concat([{ value: "custom", label: t("自定义") }])} />
                    {(customFrequency || intervalMinutes === null || ![30, 60, 360, 720, 1440].includes(intervalMinutes)) && <NumberInput
                      id={customIntervalId}
                      min={5}
                      aria-label={t("调度频率 (分钟)")}
                      max={43200}
                      value={intervalMinutes}
                      onChange={(val) => setIntervalMinutes(val)}
                      addonAfter={t("分钟")}
                    />}
                  </Field>
                ) : (
                  <>
                    <Field label={t("Cron 表达式")} htmlFor={cronInputId}>
                      <Input
                        id={cronInputId}
                        aria-label={t("Cron 表达式")}
                        value={cronExpr}
                        onChange={(e) => setCronExpr(e.target.value)}
                        placeholder="0 3 * * * / @daily / @hourly"
                        className="font-mono"
                      />
                    </Field>
                    <Field label={t("时区 (IANA)")} htmlFor={tzInputId}>
                      <Input
                        id={tzInputId}
                        aria-label={t("时区 (IANA)")}
                        value={timezone}
                        onChange={(e) => setTimezone(e.target.value)}
                        placeholder="Asia/Shanghai / UTC"
                        className="font-mono"
                      />
                    </Field>
                  </>
                )}
                {archiveMode !== "media" && (
                  <Field label={t("每轮最多用户数")}>
                    <Select ariaLabel={t("每轮目标范围")} value={limitTargets ? "limited" : "all"} onChange={value => { setLimitTargets(value === "limited"); setBatchSize(value === "all" ? 0 : 50); }} options={[{ value: "all", label: t("全部目标") }, { value: "limited", label: t("限制数量") }]} />
                    {limitTargets && <>
                    <NumberInput
                      id={batchSizeId}
                      min={1}
                      aria-label={t("每轮最多用户数")}
                      max={1000}
                      value={batchSize || null}
                      onChange={(val) => setBatchSize(val ?? 0)}
                      addonAfter={t("人")}
                    />
                    </>}
                  </Field>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}
