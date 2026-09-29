// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as commonUI from "../components/common/CommonUI";
import { TooltipProvider } from "../components/ui/Overlay";
import { toast } from "../components/ui/Toast";
import type { DownloadRecord, FailedMedia, Job } from "../lib/api";
import * as api from "../lib/api";
import { TaskCenterPage } from "./TaskCenterPage";

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return {
    ...actual,
    getJobFiles: vi.fn(),
    retryFailedTweets: vi.fn(),
  };
});

vi.mock("../components/common/CommonUI", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../components/common/CommonUI")>();
  return {
    ...actual,
    writeClipboard: vi.fn(),
  };
});

function renderWithClient(ui: React.ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>{ui}</TooltipProvider>
    </QueryClientProvider>,
  );
}

const mockJobs: Job[] = [
  {
    id: 1,
    kind: "tweet_link",
    status: "completed",
    input: "https://x.com/user/status/123",
    title: "推文 123",
    progress: 100,
    message: "下载完成",
    createdAt: "2026-09-15T08:00:00Z",
    updatedAt: "2026-09-15T08:01:00Z",
  },
  {
    id: 2,
    kind: "user",
    status: "failed",
    input: "@failed_user",
    title: "用户 failed_user",
    progress: 10,
    message: "任务失败",
    error: "用户不存在或已被封禁 (404 Not Found)",
    createdAt: "2026-09-15T09:00:00Z",
    updatedAt: "2026-09-15T09:01:00Z",
  },
  {
    id: 3,
    kind: "tweet_link",
    status: "completed_with_errors",
    input: "https://x.com/user/status/456",
    title: "推文 456",
    progress: 80,
    message: "部分媒体失败",
    error: "部分媒体链接已过期",
    createdAt: "2026-09-15T10:00:00Z",
    updatedAt: "2026-09-15T10:01:00Z",
  },
];

const mockDownloads: DownloadRecord[] = [
  {
    id: 101,
    jobId: 1,
    tweetId: "123",
    mediaUrl: "https://pbs.twimg.com/media/pic1.jpg",
    previewUrl: "",
    filePath: "/downloads/user/pic1.jpg",
    bytes: 204800,
    createdAt: "2026-09-15T08:00:30Z",
  },
  {
    id: 102,
    jobId: 1,
    tweetId: "123",
    mediaUrl: "https://video.twimg.com/video1.mp4",
    previewUrl: "",
    filePath: "/downloads/user/video1.mp4",
    bytes: 10485760,
    createdAt: "2026-09-15T08:00:45Z",
  },
];

const mockFailedMedia: FailedMedia[] = [
  {
    id: 201,
    jobId: 3,
    mediaUrl: "https://pbs.twimg.com/media/broken.jpg",
    error: "HTTP 403 Forbidden: 媒体已被删除",
    createdAt: "2026-09-15T10:00:20Z",
  },
];

describe("TaskCenterPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("在表格的执行进度列中展示错误信息并高亮异常状态", () => {
    renderWithClient(
      <TaskCenterPage
        jobs={mockJobs}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    // 错误任务应当展示具体的错误原因
    expect(screen.getByText("用户不存在或已被封禁 (404 Not Found)")).not.toBeNull();
    expect(screen.getByText("部分媒体链接已过期")).not.toBeNull();
    // 正常任务展示其 message
    expect(screen.getByText("下载完成")).not.toBeNull();
  });

  it("展开错误任务时，在详情顶部显示错误 Alert 和重试按钮", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getJobFiles).mockResolvedValue({
      downloads: [],
      failed: [],
    });

    renderWithClient(
      <TaskCenterPage
        jobs={mockJobs}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    // 点击第二个任务（失败任务）的展开按钮
    const expandButtons = screen.getAllByLabelText("展开详情");
    await user.click(expandButtons[1]);

    // 验证展示了错误横幅以及具体错误信息
    await waitFor(() => {
      expect(screen.getByText("任务执行失败")).not.toBeNull();
    });
    expect(screen.getByText("任务未生成已下载文件记录")).not.toBeNull();
    // 应当展示重试按钮
    expect(screen.getByRole("button", { name: "重试此任务" })).not.toBeNull();
  });

  it("展开成功任务时，列表形式展示已下载文件及其大小和完整路径", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getJobFiles).mockResolvedValue({
      downloads: mockDownloads,
      failed: [],
    });

    renderWithClient(
      <TaskCenterPage
        jobs={mockJobs}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    const expandButtons = screen.getAllByLabelText("展开详情");
    await user.click(expandButtons[0]);

    await waitFor(() => {
      expect(screen.getByText("pic1.jpg")).not.toBeNull();
    });
    expect(screen.getByText("video1.mp4")).not.toBeNull();
    expect(screen.getByText("/downloads/user/pic1.jpg")).not.toBeNull();
    expect(screen.getByText("/downloads/user/video1.mp4")).not.toBeNull();
    expect(screen.getByText("200.0 KB")).not.toBeNull();
    expect(screen.getByText("10.0 MB")).not.toBeNull();
  });

  it("展开含有失败媒体的任务时，展示失败媒体列表", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getJobFiles).mockResolvedValue({
      downloads: [],
      failed: mockFailedMedia,
    });

    renderWithClient(
      <TaskCenterPage
        jobs={mockJobs}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    const expandButtons = screen.getAllByLabelText("展开详情");
    await user.click(expandButtons[2]);

    await waitFor(() => {
      expect(screen.getByText("HTTP 403 Forbidden: 媒体已被删除")).not.toBeNull();
    });
    expect(screen.getByText("https://pbs.twimg.com/media/broken.jpg")).not.toBeNull();
  });

  it("状态筛选与搜索支持过滤失败任务", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <TaskCenterPage
        jobs={mockJobs}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    // 点击失败/异常分类
    const failedTab = screen.getByText(/失败\/异常/);
    await user.click(failedTab);

    // 此时应当只展示失败与部分失败任务（2个）
    expect(screen.getByText("用户 failed_user")).not.toBeNull();
    expect(screen.getByText("推文 456")).not.toBeNull();
    expect(screen.queryByText("推文 123")).toBeNull();

    // 搜索特定错误关键词
    const searchInput = screen.getByPlaceholderText(/按标题、用户名、错误信息/);
    await user.type(searchInput, "封禁");
    expect(screen.getByText("用户 failed_user")).not.toBeNull();
    expect(screen.queryByText("推文 456")).toBeNull();
  });

  it("聚合展示多账号相同报错，提供账号标签云与诊断信息复制能力", async () => {
    const user = userEvent.setup();
    const mockAggregatedJob: Job = {
      id: 320,
      kind: "following",
      status: "completed_with_errors",
      input: "@chenbin3625",
      title: "关注 @chenbin3625",
      progress: 100,
      message: "归档完成：用户 262，推文 8794，下载 101，跳过 10893，失败 31",
      error:
        "读取 @ld98625239830 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试 " +
        "读取 @Finngangpao 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试 " +
        "另有 29 个错误",
      createdAt: "2026-09-15T15:13:00Z",
      updatedAt: "2026-09-15T15:22:00Z",
    };

    vi.mocked(api.getJobFiles).mockResolvedValue({
      downloads: [],
      failed: [],
    });

    renderWithClient(
      <TaskCenterPage
        jobs={[mockAggregatedJob]}
        failedTweetCount={0}
        pagination={{ page: 1, pageSize: 20, total: 1, totalPages: 1 }}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onOpenCreateModal={vi.fn()}
        onOpenFailedDrawer={vi.fn()}
      />,
    );

    // 验证表格进度列中展示聚合后的错误原因与数量提示，而非长字符串截断
    expect(
      screen.getByText("X 客户端暂时全部限流，请稍后重试 (31项)"),
    ).not.toBeNull();

    // 展开详情
    const expandBtn = screen.getByLabelText("展开详情");
    await user.click(expandBtn);

    await waitFor(() => {
      expect(screen.getByText("任务处理存在异常或部分失败")).not.toBeNull();
    });

    // 验证聚合卡片与受影响账号标签
    expect(screen.getByText("31 个失败")).not.toBeNull();
    expect(screen.getByText("读取媒体时间线失败")).not.toBeNull();
    expect(screen.getByText("@ld98625239830")).not.toBeNull();
    expect(screen.getByText("@Finngangpao")).not.toBeNull();
    expect(
      screen.getByText("另有 29 个账号（相同原因已聚合）"),
    ).not.toBeNull();
    expect(screen.getByRole("button", { name: "复制错误信息" })).not.toBeNull();
  });
});


function renderTaskCenter(failedTweetCount: number) {
  return renderWithClient(
    <TaskCenterPage
      jobs={mockJobs}
      failedTweetCount={failedTweetCount}
      pagination={{ page: 1, pageSize: 20, total: 3, totalPages: 1 }}
      onPageChange={vi.fn()}
      onPageSizeChange={vi.fn()}
      onOpenCreateModal={vi.fn()}
      onOpenFailedDrawer={vi.fn()}
    />,
  );
}

describe("TaskCenterPage 失败推文重试", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("失败推文队列为空时不展示重试按钮，即使存在失败任务", () => {
    renderTaskCenter(0);
    expect(screen.queryByRole("button", { name: /重试失败推文|重试全部失败/ })).toBeNull();
  });

  it("失败推文队列非空时展示“重试失败推文”并调用失败推文重试接口", async () => {
    const user = userEvent.setup();
    vi.mocked(api.retryFailedTweets).mockResolvedValue({ ...mockJobs[0], id: 99, kind: "failed_retry" });
    renderTaskCenter(5);

    await user.click(screen.getByRole("button", { name: "重试失败推文" }));

    await waitFor(() => expect(api.retryFailedTweets).toHaveBeenCalledTimes(1));
  });
});

describe("TaskCenterPage 复制错误信息", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getJobFiles).mockResolvedValue({ downloads: [], failed: [] });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  async function openFailedJob() {
    const user = userEvent.setup();
    renderTaskCenter(0);
    await user.click(screen.getAllByLabelText("展开详情")[1]);
    await waitFor(() => expect(screen.getByText("任务执行失败")).not.toBeNull());
    return user;
  }

  it("通过 writeClipboard 复制并在成功后显示已复制", async () => {
    vi.mocked(commonUI.writeClipboard).mockResolvedValue(true);
    const user = await openFailedJob();

    await user.click(screen.getByRole("button", { name: "复制错误信息" }));

    expect(commonUI.writeClipboard).toHaveBeenCalledWith(expect.stringContaining("任务 ID: #2"));
    expect(await screen.findByRole("button", { name: "已复制诊断信息" })).not.toBeNull();
  });

  it("复制失败时给出警告而不是显示已复制", async () => {
    vi.mocked(commonUI.writeClipboard).mockResolvedValue(false);
    const warning = vi.spyOn(toast, "warning");
    const user = await openFailedJob();

    await user.click(screen.getByRole("button", { name: "复制错误信息" }));

    await waitFor(() => expect(warning).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: "已复制诊断信息" })).toBeNull();
    expect(screen.getByRole("button", { name: "复制错误信息" })).not.toBeNull();
  });

  it("卸载时清理复位计时器", async () => {
    vi.mocked(commonUI.writeClipboard).mockResolvedValue(true);
    const setTimeoutSpy = vi.spyOn(window, "setTimeout");
    const clearTimeoutSpy = vi.spyOn(window, "clearTimeout");
    const user = await openFailedJob();
    await user.click(screen.getByRole("button", { name: "复制错误信息" }));
    await screen.findByRole("button", { name: "已复制诊断信息" });

    const resetCall = setTimeoutSpy.mock.calls.findIndex(([, delay]) => delay === 2000);
    expect(resetCall).toBeGreaterThanOrEqual(0);
    const resetTimer = setTimeoutSpy.mock.results[resetCall].value;

    cleanup();

    expect(clearTimeoutSpy).toHaveBeenCalledWith(resetTimer);
  });
});
