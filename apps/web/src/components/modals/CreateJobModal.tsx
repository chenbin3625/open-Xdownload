import {
  CloudDownloadOutlined,
  CopyOutlined,
  FileTextOutlined,
  LinkOutlined,
  LoadingOutlined,
  ScheduleOutlined,
  TeamOutlined,
  UnorderedListOutlined,
  UserAddOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Tabs,
  Typography,
  notification,
} from "antd";
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
import { getErrorMessage } from "../common/CommonUI";
import { invalidateWorkbenchQueries } from "../../lib/useDashboardEvents";

const { Text, Paragraph } = Typography;
const { TextArea } = Input;

export interface CreateJobModalProps {
  open: boolean;
  onClose: () => void;
  initialInput?: string;
  initialKind?: JobKind | "schedule";
}

const tabKeys: string[] = ["user", "tweet_link", "list", "following"];

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

  // 输入状态
  const [tweetUrl, setTweetUrl] = useState("");
  const [userInputs, setUserInputs] = useState("");
  const [listInputs, setListInputs] = useState("");
  const [followingInputs, setFollowingInputs] = useState("");

  // 单推文解析结果缓存
  const [parsedTweet, setParsedTweet] = useState<TweetData | null>(null);

  // 定时计划开关与参数
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
        setListInputs(initialInput);
      } else {
        setActiveTab("user");
        setUserInputs(initialInput);
      }
    } else {
      // 侧边栏入口没有初始输入，此时若不复位就会停在上一次会话的分页上。
      // media_url / failed_retry 没有对应分页，统一落回 user。
      setActiveTab(tabKeys.includes(initialKind) ? initialKind : "user");
    }
    if (initialKind === "schedule") {
      setIsSchedule(true);
    }
  }, [open, initialInput, initialKind]);

  // 解析目标项计算
  const currentBatchItems = useMemo(() => {
    switch (activeTab) {
      case "user":
        return parseLinesToItems(userInputs, "user");
      case "list":
        return parseLinesToItems(listInputs, "list");
      case "following":
        return parseLinesToItems(followingInputs, "following");
      default:
        return [];
    }
  }, [activeTab, userInputs, listInputs, followingInputs]);

  // 单推文解析 Mutation
  const parseMutation = useMutation({
    mutationFn: parseTweetLink,
    onSuccess: (data) => {
      setParsedTweet(data);
      notification.success({
        message: "推文解析成功",
        description: `包含 ${data.media.length} 个媒体，作者 @${data.author.screenName}`,
      });
    },
    onError: (err) => {
      notification.error({
        message: "解析失败",
        description: getErrorMessage(err),
      });
    },
  });

  // 创建任务 / 批量任务 Mutation
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
        notification.success({
          message: "定时归档计划已保存",
          description: `执行频率: 每 ${intervalMinutes} 分钟`,
        });
      } else {
        notification.success({
          message: "任务创建成功",
          description: `已成功入库 ${jobs.length} 个任务并开始后台下载`,
        });
      }
      handleClose();
    },
    onError: (err) => {
      notification.error({
        message: "创建失败",
        description: getErrorMessage(err),
      });
    },
  });

  const handleClose = () => {
    setTweetUrl("");
    setUserInputs("");
    setListInputs("");
    setFollowingInputs("");
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

  const tabItems = [
    {
      key: "user",
      label: (
        <span className="flex items-center gap-1.5">
          <UserOutlined />
          <span>用户归档</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex items-center justify-between" style={{ fontSize: 12, color: "var(--text-muted)" }}>
            <span>支持用户名、@screen_name 或数字 ID，每行一个</span>
            <span className="font-mono" style={{ color: "var(--brand-500)" }}>
              已识别: {currentBatchItems.length} 个
            </span>
          </div>
          <TextArea
            rows={6}
            value={userInputs}
            onChange={(e) => setUserInputs(e.target.value)}
            placeholder={"elonmusk\n@sama\n44196397\nhttps://x.com/OpenAI"}
            className="font-mono" style={{ fontSize: 12 }}
          />
        </div>
      ),
    },
    {
      key: "tweet_link",
      label: (
        <span className="flex items-center gap-1.5">
          <LinkOutlined />
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
              style={{ fontSize: 12 }}
            />
            <Button
              type="primary"
              disabled={!tweetUrl.trim()}
              loading={parseMutation.isPending}
              onClick={handleParseTweet}
              className="shrink-0"
            >
              解析推文
            </Button>
          </div>

          {parsedTweet && (
            <div className="p-3 rounded-lg space-y-2" style={{ background: "var(--app-surface-muted)", border: "1px solid var(--app-border)", fontSize: 12 }}>
              <div className="flex items-center justify-between">
                <span style={{ fontWeight: 600 }}>
                  @{parsedTweet.author.screenName} ({parsedTweet.author.name})
                </span>
                <span style={{ color: "#10b981", fontWeight: 500 }}>
                  {parsedTweet.media.length} 个可用媒体
                </span>
              </div>
              <Paragraph
                ellipsis={{ rows: 2 }}
                style={{ fontSize: 12 }}
              >
                {parsedTweet.text || "无正文"}
              </Paragraph>
              <div className="flex items-center gap-2" style={{ fontSize: 11, color: "var(--text-muted)" }}>
                <span>推文 ID: {parsedTweet.id}</span>
                <span>·</span>
                <span>发布于: {parsedTweet.createdAt || "未知"}</span>
              </div>
            </div>
          )}
        </div>
      ),
    },
    {
      key: "list",
      label: (
        <span className="flex items-center gap-1.5">
          <UnorderedListOutlined />
          <span>列表归档</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex items-center justify-between" style={{ fontSize: 12, color: "var(--text-muted)" }}>
            <span>输入 X 列表 ID 或完整列表 URL，自动获取列表成员推文媒体</span>
            <span className="font-mono" style={{ color: "var(--brand-500)" }}>
              已识别: {currentBatchItems.length} 个
            </span>
          </div>
          <TextArea
            rows={6}
            value={listInputs}
            onChange={(e) => setListInputs(e.target.value)}
            placeholder={"1492019283\nhttps://x.com/i/lists/1647289190"}
            className="font-mono" style={{ fontSize: 12 }}
          />
        </div>
      ),
    },
    {
      key: "following",
      label: (
        <span className="flex items-center gap-1.5">
          <UserAddOutlined />
          <span>关注关系归档</span>
        </span>
      ),
      children: (
        <div className="space-y-3 pt-1">
          <div className="flex items-center justify-between" style={{ fontSize: 12, color: "var(--text-muted)" }}>
            <span>输入目标账号，自动获取其关注的所有账号并进行媒体归档</span>
            <span className="font-mono" style={{ color: "var(--brand-500)" }}>
              已识别: {currentBatchItems.length} 个
            </span>
          </div>
          <TextArea
            rows={6}
            value={followingInputs}
            onChange={(e) => setFollowingInputs(e.target.value)}
            placeholder={"elonmusk\n@OpenAI"}
            className="font-mono" style={{ fontSize: 12 }}
          />
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
      onCancel={handleClose}
      footer={null}
      width={680}
      title={
        <div className="flex items-center gap-2.5 py-1">
          <div className="w-8 h-8 rounded-lg flex items-center justify-center" style={{ background: "var(--brand-100)", color: "var(--brand-600)", fontWeight: 700 }}>
            +
          </div>
          <div>
            <div style={{ fontWeight: 600, fontSize: 16 }}>
              新建下载任务 / 归档计划
            </div>
            <div style={{ fontSize: 12, color: "var(--text-muted)", fontWeight: 400 }}>
              支持单条推文、批量用户时间线、列表与关注关系媒体高速入库
            </div>
          </div>
        </div>
      }
    >
      <div className="py-2 space-y-4">
        <Tabs
          activeKey={activeTab}
          onChange={(key) => {
            setActiveTab(key);
            setParsedTweet(null);
          }}
          items={tabItems}
        />

        {/* 存为定时归档计划选项 (单条推文除外) */}
        {activeTab !== "tweet_link" && (
          <div className="p-3.5 rounded-lg space-y-3" style={{ background: "var(--app-surface-muted)", border: "1px solid var(--app-border)" }}>
            <div className="flex items-center justify-between">
              <span className="flex items-center gap-1.5" style={{ fontSize: 12, fontWeight: 600 }}>
                <ScheduleOutlined style={{ color: "var(--brand-500)" }} />
                是否保存为自动归档计划（定期抓取）？
              </span>
              <Checkbox
                checked={isSchedule}
                onChange={(e) => setIsSchedule(e.target.checked)}
              />
            </div>

            {isSchedule && (
              <div className="pt-2 grid grid-cols-1 sm:grid-cols-2 gap-3" style={{ borderTop: "1px solid var(--app-border)", fontSize: 12 }}>
                <div>
                  <div className="mb-1" style={{ color: "var(--text-muted)" }}>
                    计划名称
                  </div>
                  <Input
                    value={scheduleName}
                    onChange={(e) => setScheduleName(e.target.value)}
                    placeholder="例如：重点博主媒体日常归档"
                    size="small"
                    
                  />
                </div>
                <div>
                  <div className="mb-1" style={{ color: "var(--text-muted)" }}>
                    调度频率 (分钟)
                  </div>
                  <InputNumber
                    min={5}
                    max={43200}
                    value={intervalMinutes}
                    onChange={(val) => setIntervalMinutes(val ?? 360)}
                    size="small"
                    addonAfter="分钟"
                    style={{ width: "100%" }}
                  />
                </div>
              </div>
            )}
          </div>
        )}

        {/* 底部按钮栏 */}
        <div className="flex items-center justify-between pt-3" style={{ borderTop: "1px solid var(--app-border)" }}>
          <span style={{ fontSize: 12, color: "var(--text-subtle)" }}>
            {activeTab === "tweet_link"
              ? "解析后点击立即下载入库"
              : isSchedule
              ? `将保存为包含 ${currentBatchItems.length} 个目标的定时计划`
              : `将同时创建 ${currentBatchItems.length} 个后台下载任务`}
          </span>
          <Space size={8}>
            <Button onClick={handleClose}>
              取消
            </Button>
            <Button
              type="primary"
              disabled={!canSubmit}
              loading={createJobsMutation.isPending}
              onClick={() => createJobsMutation.mutate()}
              icon={<CloudDownloadOutlined />}
            >
              {isSchedule
                ? "保存定时计划"
                : activeTab === "tweet_link"
                ? "立即下载"
                : `批量下载 (${currentBatchItems.length})`}
            </Button>
          </Space>
        </div>
      </div>
    </Modal>
  );
}
