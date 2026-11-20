import { t, useTranslation } from "../../lib/i18n";
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
import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  createArchiveSchedule,
  createJob,
  createJobsBatch,
  parseTweetLink,
  type ArchiveScheduleRequest,
  type JobKind,
  type JobRequest,
  type TweetData,
  type ArchiveMode,
} from "../../lib/api";
import { formatIntervalMinutes, notifyError } from "../common/CommonUI";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";
import { Button, RadioGroup } from "@douyinfe/semi-ui";
import { Checkbox, Tabs, type TabItem } from "../semi";
import { Input, NumberInput, Textarea } from "../semi";
import { Modal } from "../semi";
import { Spinner } from "../semi";
import { toast } from "../semi";
import { Field, Select, Tag } from "../semi";

export interface CreateJobModalProps {
  open: boolean;
  onClose: () => void;
  initialInput?: string;
  initialKind?: JobKind | "schedule";
}

const tabKeys: string[] = ["user", "tweet_link", "list", "following"];

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
const tweetStatusPattern = /\/status\/\d+/;
const listLinkPattern = /(?:^|\/)(?:i\/)?lists\/\d+/;

function normalizeTweetURL(input: string) {
  const trimmed = input.trim();
  return xLinkPattern.test(trimmed) && !/^https?:\/\//i.test(trimmed) ? `https://${trimmed}` : trimmed;
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

// 顶部快捷输入框的分流：推文链接 → 单条推文，列表链接/纯数字 → 列表，其余 → 用户。
export function detectQuickInputTab(input: string): "tweet_link" | "list" | "user" {
  const trimmed = input.trim();
  if (tweetStatusPattern.test(trimmed)) return "tweet_link";
  // 纯数字 ID 更常见的是用户 ID（列表 ID 也会走到这里，后端两种都能解析；
  // 之前的实现判成 list，导致粘贴用户 ID 后必须手动切 tab）。
  if (/^\d{5,}$/.test(trimmed)) return "user";
  if (listLinkPattern.test(trimmed)) return "list";
  return "user";
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

export function CreateJobModal({
  open,
  onClose,
  initialInput = "",
  initialKind = "user",
}: CreateJobModalProps) {
  useTranslation();
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState<string>("user");
  const [archiveMode, setArchiveMode] = useState<ArchiveMode>("both");
  const [backfill, setBackfill] = useState(false);
  const [batchSize, setBatchSize] = useState(0);
  const [limitTargets, setLimitTargets] = useState(false);

  const [tweetUrl, setTweetUrl] = useState("");
  const [batchInputs, setBatchInputs] = useState<Partial<Record<BatchKind, string>>>({});

  const [parsedTweet, setParsedTweet] = useState<TweetData | null>(null);
  const parseSequence = useRef(0);
  const createSequence = useRef(0);

  const [isSchedule, setIsSchedule] = useState(false);
  const [scheduleName, setScheduleName] = useState("");
  const [intervalMinutes, setIntervalMinutes] = useState(360);
  const [customFrequency, setCustomFrequency] = useState(false);

  useEffect(() => {
    parseSequence.current += 1;
    createSequence.current += 1;
    setParsedTweet(null);
    if (!open) return;
    if (initialInput) {
      const quickTab = detectQuickInputTab(initialInput);
      setActiveTab(quickTab);
      if (quickTab === "tweet_link") {
        setTweetUrl(normalizeTweetURL(initialInput));
      } else {
        setBatchInputs({ [quickTab]: initialInput });
      }
    } else if (!Object.values(batchInputs).some(value => value?.trim()) && !tweetUrl.trim()) {
      setActiveTab(tabKeys.includes(initialKind) ? initialKind : "user");
    }
    if (initialKind === "schedule") {
      setIsSchedule(true);
      setActiveTab(current => current === "tweet_link" ? "user" : current);
    } else {
      setIsSchedule(false);
    }
  }, [open, initialInput, initialKind]);

  // 单条推文不支持定时计划：切到该标签时勾选状态保留（切回可恢复），但不参与任何展示与提交。
  const scheduleMode = isSchedule && activeTab !== "tweet_link";

  const currentBatchSummary = useMemo(() => {
    if (activeTab === "tweet_link") return { items: [], recognized: 0, pendingValidation: 0, duplicates: 0, invalid: 0, lines: [] };
    const raw = batchInputs[activeTab as BatchKind] ?? "";
    return parseBatchInput(raw, activeTab as JobKind);
  }, [activeTab, batchInputs]);
  const currentBatchItems = currentBatchSummary.items;

  function removeBatchLine(kind: BatchKind, lineNumber: number) {
    const lines = (batchInputs[kind] ?? "").split(/\r?\n/);
    lines.splice(lineNumber - 1, 1);
    setBatchInputs(current => ({ ...current, [kind]: lines.join("\n") }));
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
    onMutate: () => ({ sequence: createSequence.current, scheduleMode, intervalMinutes }),
    mutationFn: async () => {
      if (activeTab === "tweet_link") {
        const targetUrl = normalizeTweetURL(tweetUrl);
        if (!targetUrl) throw new Error(t("请输入推文链接"));
        const title = parsedTweet
          ? t("推文 @{{value0}} ({{value1}})", { value0: parsedTweet.author.screenName, value1: parsedTweet.id })
          : `Tweet ${targetUrl.split("/").pop() || targetUrl}`;
        return [await createJob("tweet_link", targetUrl, title, { archiveMode, backfill })];
      }

      if (currentBatchItems.length === 0) {
        throw new Error(t("请至少输入一个有效目标"));
      }

      if (scheduleMode) {
        const payload: ArchiveScheduleRequest = {
          name:
            scheduleName.trim() ||
            t("自动归档 ({{value0}} 个目标)", { value0: currentBatchItems.length }),
          enabled: true,
          intervalMinutes,
          items: currentBatchItems.map(item => ({
            ...item, archiveMode, backfill,
            batchSize: archiveMode === "media" ? 0 : batchSize,
          })),
        };
        await createArchiveSchedule(payload);
        return [];
      }

      return await createJobsBatch({ items: currentBatchItems.map(item => ({ ...item, archiveMode, backfill })) });
    },
    onSuccess: (jobs, _variables, request) => {
      void invalidateWorkbenchQueries(queryClient);
      if (request?.sequence !== createSequence.current) return;
      if (request.scheduleMode) {
        toast.success({
          message: t("定时归档计划已保存"),
          description: formatIntervalMinutes(request.intervalMinutes),
        });
      } else {
        toast.success({
          message: t("任务创建成功"),
          description: t("已成功入库 {{value0}} 个任务并开始后台下载", { value0: jobs.length }),
        });
      }
      setTweetUrl(""); setBatchInputs({}); setScheduleName(""); setIsSchedule(false);
      handleClose();
    },
    onError: (error, _variables, request) => {
      if (request?.sequence === createSequence.current) notifyError(t("创建失败"))(error);
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
    const trimmed = normalizeTweetURL(tweetUrl);
    if (!trimmed) return;
    setParsedTweet(null);
    parseSequence.current += 1;
    parseMutation.mutate({ url: trimmed, sequence: parseSequence.current });
  };

  const tabItems: TabItem[] = [
    ...getBatchTabs().map((tab) => ({
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
            <span className="shrink-0 whitespace-nowrap font-semibold text-brand-600 tabular-nums">
              {t("待校验目标")} {currentBatchItems.length} {t(" 个")}</span>
          </div>
          {(currentBatchSummary.recognized > 0 || currentBatchSummary.pendingValidation > 0 || currentBatchSummary.duplicates > 0 || currentBatchSummary.invalid > 0) && (
            <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-fg-subtle tabular-nums" aria-live="polite">
              {currentBatchSummary.recognized > 0 && <span>{t("格式有效")} {currentBatchSummary.recognized}</span>}
              {currentBatchSummary.pendingValidation > 0 && <span>{t("待后端校验")} {currentBatchSummary.pendingValidation}</span>}
              {currentBatchSummary.duplicates > 0 && <span className="text-warning-strong">{t("重复目标")} {currentBatchSummary.duplicates}</span>}
              {currentBatchSummary.invalid > 0 && <span className="text-danger">{t("无效格式")} {currentBatchSummary.invalid}</span>}
            </div>
          )}
          <Textarea
            rows={6}
            aria-label={tab.label}
            value={batchInputs[tab.kind] ?? ""}
            onChange={(e) =>
              setBatchInputs((current) => ({ ...current, [tab.kind]: e.target.value }))
            }
            placeholder={tab.placeholder}
            className="font-mono text-xs"
          />
          {currentBatchSummary.lines.length > 0 && (
            <div className="space-y-1.5" aria-label={t("逐行校验结果")}>
              {currentBatchSummary.lines.map(line => (
                <div key={`${tab.kind}-${line.lineNumber}`} className="flex min-w-0 items-center gap-2 text-xs">
                  <span className="w-12 shrink-0 text-fg-subtle tabular-nums">{t("第 {{value0}} 行", { value0: line.lineNumber })}</span>
                  <span className="min-w-0 flex-1 truncate font-mono text-fg-muted" title={line.raw}>{line.normalized || line.raw}</span>
                  {line.status === "valid" && <Tag tone="success" size="sm">{t("格式有效")}</Tag>}
                  {line.status === "pending" && <Tag tone="warning" size="sm">{t("待后端校验")}</Tag>}
                  {line.status === "invalid" && <Tag tone="danger" size="sm">{t("无效格式，已忽略")}</Tag>}
                  {line.status === "duplicate" && <><Tag tone="danger" size="sm">{t("与第 {{value0}} 行重复，已忽略", { value0: line.duplicateOf })}</Tag><Button type="tertiary" size="small" onClick={() => removeBatchLine(tab.kind, line.lineNumber)}>{t("移除重复行")}</Button></>}
                </div>
              ))}
            </div>
          )}
        </div>
      ),
    })),
    {
      key: "tweet_link",
      label: (
        <span className="flex items-center gap-1.5">
          <LinkIcon className="size-4" />
          <span>{t("单条推文")}</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex gap-2">
            <Input
              value={tweetUrl}
              onChange={(e) => {
                parseSequence.current += 1;
                setTweetUrl(e.target.value);
                setParsedTweet(null);
              }}
              onPressEnter={handleParseTweet}
              placeholder="https://x.com/username/status/1234567890"
            />
            <Button
              theme="solid"
              disabled={!tweetUrl.trim() || parseMutation.isPending}
              loading={parseMutation.isPending}
              aria-busy={parseMutation.isPending}
              onClick={handleParseTweet}
              className="shrink-0"
            >
              {t("解析推文")}</Button>
          </div>

          {parsedTweet && (
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
      className="create-job-modal sm:w-[min(92vw,44rem)]"
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
            {activeTab === "tweet_link"
              ? t("解析后点击立即下载入库")
              : scheduleMode
              ? t("将保存为包含 {{value0}} 个目标的定时计划", { value0: currentBatchItems.length })
              : t("将同时创建 {{value0}} 个后台下载任务", { value0: currentBatchItems.length })}
          </span>
          <div className="flex items-center justify-end gap-2">
            <Button type="tertiary" onClick={handleClose}>
              {t("取消")}</Button>
            <Button
              theme="solid"
              disabled={!canSubmit || createJobsMutation.isPending || scheduleMode && (!Number.isInteger(intervalMinutes) || intervalMinutes < 5 || intervalMinutes > 43200 || archiveMode !== "media" && limitTargets && (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 1000))}
              loading={createJobsMutation.isPending}
              aria-busy={createJobsMutation.isPending}
              onClick={() => createJobsMutation.mutate()}
              icon={<Download className="size-4" />}
            >
              {scheduleMode
                ? t("保存定时计划")
                : activeTab === "tweet_link"
                ? t("立即下载")
                : t("批量下载 ({{value0}})", { value0: currentBatchItems.length })}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-4 py-1">
        {activeTab !== "tweet_link" && <RadioGroup aria-label={t("执行方式")} type="button" value={isSchedule ? "schedule" : "once"} onChange={event => setIsSchedule(event.target.value === "schedule")} options={[{ value: "once", label: t("立即执行") }, { value: "schedule", label: t("定时计划") }]} />}
        <Field label={t("archive.mode")} className="mb-4">
          <Select value={archiveMode} onChange={value => setArchiveMode(value as ArchiveMode)} ariaLabel={t("archive.mode")} options={[{ value: "both", label: t("archive.both") }, { value: "tweets", label: t("archive.textOnly") }, { value: "media", label: t("archive.mediaOnly") }]} />
        </Field>
        {activeTab !== "tweet_link" && archiveMode !== "media" && <div className="mb-4"><Checkbox checked={backfill} onChange={setBackfill} label={t("archive.backfill")} /></div>}
        <Tabs
          value={activeTab}
          onChange={(key) => {
            setActiveTab(key);
            setParsedTweet(null);
          }}
          items={tabItems}
        />

        {/* 存为定时归档计划选项 (单条推文除外) */}
        {scheduleMode && (
          <div className="space-y-3 rounded-card border border-line bg-surface-muted/50 p-3.5">
            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-center gap-2">
                <Clock className="size-4 shrink-0 text-brand-500" />
                <span className="text-xs font-semibold text-fg">
                  {t("调度设置")}</span>
              </div>
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
                <Field label={t("执行频率")}>
                  <Select ariaLabel={t("常用执行频率")} value={!customFrequency && [30, 60, 360, 720, 1440].includes(intervalMinutes) ? String(intervalMinutes) : "custom"} onChange={value => { setCustomFrequency(value === "custom"); if (value !== "custom") setIntervalMinutes(Number(value)); }} options={[30, 60, 360, 720, 1440].map(value => ({ value: String(value), label: formatIntervalMinutes(value) })).concat([{ value: "custom", label: t("自定义") }])} />
                  {(customFrequency || ![30, 60, 360, 720, 1440].includes(intervalMinutes)) && <NumberInput
                    min={5}
                    aria-label={t("调度频率 (分钟)")}
                    max={43200}
                    value={intervalMinutes}
                    onChange={(val) => setIntervalMinutes(val ?? 360)}
                    addonAfter={t("分钟")}
                  />}
                </Field>
                {archiveMode !== "media" && (
                  <Field label={t("每轮最多用户数")}>
                    <Select ariaLabel={t("每轮目标范围")} value={limitTargets ? "limited" : "all"} onChange={value => { setLimitTargets(value === "limited"); setBatchSize(value === "all" ? 0 : 50); }} options={[{ value: "all", label: t("全部目标") }, { value: "limited", label: t("限制数量") }]} />
                    {limitTargets && <>
                    <NumberInput
                      min={0}
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
