// @vitest-environment jsdom
import React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { act, renderWithProviders, screen, waitFor } from "../test/render";
import * as api from "../lib/api";
import { SchedulesPage } from "./SchedulesPage";

vi.mock("../lib/api", async original => ({
  ...await original<typeof import("../lib/api")>(),
  updateArchiveSchedule: vi.fn(), runArchiveSchedule: vi.fn(), deleteArchiveSchedule: vi.fn(),
}));

const schedule: api.ArchiveSchedule = {
  id: 1, name: "Mixed archive", enabled: false, intervalMinutes: 90,
  items: [
    { kind: "user", input: "alice", title: "User alice", archiveMode: "tweets", backfill: true, batchSize: 37 },
    { kind: "list", input: "123456", title: "List archive", archiveMode: "media", backfill: false, batchSize: 0 },
  ],
  nextRunAt: "2026-11-14T12:00:00Z", lastJobIds: [9], createdAt: "", updatedAt: "",
};

function renderSchedules(schedules = [schedule]) {
  return renderWithProviders(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <SchedulesPage schedules={schedules} onOpenCreateModal={vi.fn()} />
  </QueryClientProvider>);
}

beforeEach(() => vi.clearAllMocks());

it("edits mixed targets without losing per-target options or rounding the interval", async () => {
  const user = userEvent.setup();
  vi.mocked(api.updateArchiveSchedule).mockResolvedValue(schedule);
  renderSchedules();
  await user.click(screen.getByRole("button", { name: "编辑计划 Mixed archive" }));
  await user.clear(screen.getByRole("textbox", { name: "目标 1" }));
  await user.type(screen.getByRole("textbox", { name: "目标 1" }), "bob");
  await user.click(screen.getByRole("button", { name: "保存计划" }));
  await waitFor(() => expect(api.updateArchiveSchedule).toHaveBeenCalledWith(1, {
    name: "Mixed archive", enabled: false, intervalMinutes: 90,
    items: [{ ...schedule.items[0], input: "bob", title: undefined }, schedule.items[1]],
  }));
});

it("blocks repeated executions across rows while a request is pending", async () => {
  const user = userEvent.setup();
  let finish!: (jobs: api.Job[]) => void;
  vi.mocked(api.runArchiveSchedule).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  renderSchedules([schedule, { ...schedule, id: 2, name: "Second archive" }]);
  const first = screen.getByRole("button", { name: "立即执行 Mixed archive" }) as HTMLButtonElement;
  await user.click(first);
  await waitFor(() => expect((screen.getByRole("button", { name: "立即执行 Mixed archive" }) as HTMLButtonElement).disabled).toBe(true));
  expect((screen.getByRole("button", { name: "立即执行 Second archive" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "立即执行 Mixed archive" }));
  await user.click(screen.getByRole("button", { name: "立即执行 Second archive" }));
  expect(api.runArchiveSchedule).toHaveBeenCalledTimes(1);
  await act(async () => finish([]));
  await waitFor(() => expect((screen.getByRole("button", { name: "立即执行 Second archive" }) as HTMLButtonElement).disabled).toBe(false));
});

it("filters schedules by target and restores the list from an empty result", async () => {
  const user = userEvent.setup(); renderSchedules();
  const search = screen.getByRole("textbox", { name: "搜索计划" });
  await user.type(search, "alice");
  expect(screen.getByRole("button", { name: "Mixed archive" })).toBeTruthy();
  await user.clear(search); await user.type(search, "missing");
  expect(screen.getByText("没有匹配的计划")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "清除筛选" }));
  expect(screen.getByRole("button", { name: "Mixed archive" })).toBeTruthy();
});

it("disables per-target remove buttons while saving an edited schedule", async () => {
  const user = userEvent.setup();
  let finish!: (value: api.ArchiveSchedule) => void;
  vi.mocked(api.updateArchiveSchedule).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  renderSchedules();
  await user.click(screen.getByRole("button", { name: "编辑计划 Mixed archive" }));
  const removeBtn = screen.getByRole("button", { name: "移除目标 1" }) as HTMLButtonElement;
  expect(removeBtn.disabled).toBe(false);
  await user.click(screen.getByRole("button", { name: "保存计划" }));
  await waitFor(() => expect((screen.getByRole("button", { name: "移除目标 1" }) as HTMLButtonElement).disabled).toBe(true));
  await act(async () => finish(schedule));
});

it("does not coerce cleared interval to 0 and trims target inputs before submission", async () => {
  const user = userEvent.setup();
  vi.mocked(api.updateArchiveSchedule).mockResolvedValue(schedule);
  renderSchedules();

  await user.click(screen.getByRole("button", { name: "编辑计划 Mixed archive" }));
  const intervalInput = screen.getByRole("spinbutton", { name: "调度频率 (分钟)" }) as HTMLInputElement;
  await user.clear(intervalInput);
  expect(intervalInput.value).toBe("");
  expect((screen.getByRole("button", { name: "保存计划" }) as HTMLButtonElement).disabled).toBe(true);

  await user.type(intervalInput, "30");
  const nameInput = screen.getByRole("textbox", { name: "计划名称" });
  await user.clear(nameInput);
  await user.type(nameInput, "  Trimmed schedule  ");
  const targetInput = screen.getByRole("textbox", { name: "目标 1" });
  await user.clear(targetInput);
  await user.type(targetInput, "  @charlie  ");

  await user.click(screen.getByRole("button", { name: "保存计划" }));
  await waitFor(() => expect(api.updateArchiveSchedule).toHaveBeenCalledWith(1, {
    name: "Trimmed schedule",
    enabled: false,
    intervalMinutes: 30,
    items: [
      { ...schedule.items[0], input: "@charlie", title: undefined },
      schedule.items[1],
    ],
  }));
});
