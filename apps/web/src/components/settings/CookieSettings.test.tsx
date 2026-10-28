// @vitest-environment jsdom
import { Tooltip } from "radix-ui";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  BackupCookieInputs,
  CookieSettingsFields,
  emptyBackupCookieRow,
  normalizeBackupCookieRows,
  parseBackupCookieRows,
  parsePastedCookieRows,
} from "./CookieSettings";
import type { AppConfig } from "../../lib/api";

afterEach(cleanup);

it("allows clearing an incomplete primary cookie", () => {
  const onChange = vi.fn();
  const draft = { authToken: "********", csrfToken: "" } as AppConfig;
  render(<Tooltip.Provider><CookieSettingsFields authError="" authResult={null} checking={false} draft={draft} onChange={onChange} /></Tooltip.Provider>);
  fireEvent.click(screen.getByRole("button", { name: /^清除$/ }));
  expect(onChange.mock.calls[0][0](draft)).toMatchObject({ authToken: "__CLEAR__", csrfToken: "__CLEAR__" });
});

describe("backup cookie identities", () => {
  it("does not reuse a saved row ID after the module reloads", async () => {
    const saved = { ...emptyBackupCookieRow(), authToken: "********", csrfToken: "********" };
    vi.resetModules();
    const reloaded = await import("./CookieSettings");
    const rows = reloaded.parseBackupCookieRows(JSON.stringify([saved]));
    const added = reloaded.emptyBackupCookieRow();

    expect(added.id).not.toBe(rows[0].id);
    expect(rows[0]).toEqual(saved);
  });

  it("preserves saved row identities when an earlier redacted cookie is removed", () => {
    const rows = parseBackupCookieRows(JSON.stringify([
      { id: "cookie-0", authToken: "********", csrfToken: "********" },
      { id: "cookie-1", authToken: "********", csrfToken: "********" },
      { id: "cookie-2", authToken: "********", csrfToken: "********" },
    ]));

    expect(normalizeBackupCookieRows(rows.slice(1))).toBe(JSON.stringify([
      { id: "cookie-1", authToken: "********", csrfToken: "********" },
      { id: "cookie-2", authToken: "********", csrfToken: "********" },
    ]));
  });
});

describe("parsePastedCookieRows", () => {
  it("按行解析 auth_token=..; ct0=.. 文本", () => {
    const { rows, error } = parsePastedCookieRows(
      "auth_token=AAA; ct0=CT0A\nauth_token=BBB; ct0=CT0B",
    );
    expect(error).toBe("");
    expect(rows.map(({ authToken, csrfToken }) => ({ authToken, csrfToken }))).toEqual([
      { authToken: "AAA", csrfToken: "CT0A" },
      { authToken: "BBB", csrfToken: "CT0B" },
    ]);
    // 每行都是独立 id，不能复用
    expect(rows[0].id).not.toBe(rows[1].id);
  });

  it("兼容 markdown 列表前缀与逗号分隔", () => {
    const { rows } = parsePastedCookieRows("- auth_token=AAA, ct0=CT0A\n* ct0=CT0B; auth_token=BBB");
    expect(rows.map(({ authToken, csrfToken }) => ({ authToken, csrfToken }))).toEqual([
      { authToken: "AAA", csrfToken: "CT0A" },
      { authToken: "BBB", csrfToken: "CT0B" },
    ]);
  });

  it("解析 JSON 数组（含 auth_token/ct0 旧键名）", () => {
    const { rows, error } = parsePastedCookieRows(
      JSON.stringify([{ auth_token: "AAA", ct0: "CT0A" }, { authToken: "BBB", csrfToken: "CT0B" }]),
    );
    expect(error).toBe("");
    expect(rows.map(({ authToken, csrfToken }) => ({ authToken, csrfToken }))).toEqual([
      { authToken: "AAA", csrfToken: "CT0A" },
      { authToken: "BBB", csrfToken: "CT0B" },
    ]);
  });

  it("空内容与无法识别的内容给出可读错误", () => {
    expect(parsePastedCookieRows("   ").rows).toHaveLength(0);
    expect(parsePastedCookieRows("   ").error).not.toBe("");
    expect(parsePastedCookieRows("hello world").rows).toHaveLength(0);
    expect(parsePastedCookieRows("[not json").error).toContain("JSON");
  });

  it("半行的粘贴可以导入，但给出补全警告", () => {
    const { rows, error } = parsePastedCookieRows("auth_token=ONLY_TOKEN");
    expect(rows).toHaveLength(1);
    expect(error).toContain("只识别到一个字段");
  });
});

describe("BackupCookieInputs 批量粘贴", () => {
  function renderInputs(value = "") {
    const onChange = vi.fn();
    render(
      <Tooltip.Provider>
        <BackupCookieInputs
          checked={false}
          checking={false}
          clients={[]}
          errorMessage=""
          value={value}
          onChange={onChange}
        />
      </Tooltip.Provider>,
    );
    return onChange;
  }

  it("粘贴多行后导入为多行输入框，且回传带 id 的 JSON", () => {
    const onChange = renderInputs();

    fireEvent.click(screen.getByRole("button", { name: /批量粘贴/ }));
    const textarea = screen.getByRole("textbox", { name: "批量粘贴 Cookie 内容" });
    fireEvent.change(textarea, {
      target: { value: "auth_token=AAA; ct0=CT0A\nauth_token=BBB; ct0=CT0B" },
    });
    fireEvent.click(screen.getByRole("button", { name: /导入 2 个账号/ }));

    expect(onChange).toHaveBeenCalledTimes(1);
    const parsed = JSON.parse(onChange.mock.calls[0][0] as string) as Array<{
      id: string;
      authToken: string;
      csrfToken: string;
    }>;
    expect(parsed.map(({ authToken, csrfToken }) => ({ authToken, csrfToken }))).toEqual([
      { authToken: "AAA", csrfToken: "CT0A" },
      { authToken: "BBB", csrfToken: "CT0B" },
    ]);
    // id 必须是稳定字符串（后端据此回填，不能是随机的空值）
    expect(parsed.every((row) => typeof row.id === "string" && row.id.length > 0)).toBe(true);

    // 导入后界面应渲染两行账号、并收起粘贴面板
    expect(screen.getByLabelText("备用 Cookie 1 auth_token")).not.toBeNull();
    expect(screen.getByLabelText("备用 Cookie 2 auth_token")).not.toBeNull();
    expect(screen.queryByRole("textbox", { name: "批量粘贴 Cookie 内容" })).toBeNull();
  });

  it("粘贴内容不可识别时禁用导入", () => {
    renderInputs();
    fireEvent.click(screen.getByRole("button", { name: /批量粘贴/ }));
    fireEvent.change(screen.getByRole("textbox", { name: "批量粘贴 Cookie 内容" }), {
      target: { value: "这不是 cookie" },
    });
    const importButton = screen.getByRole("button", { name: /导入/ }) as HTMLButtonElement;
    expect(importButton.disabled).toBe(true);
  });

  it("半行在行内给出警告标签", () => {
    renderInputs(JSON.stringify([{ id: "cookie-0", authToken: "ONLY_AUTH", csrfToken: "" }]));
    expect(screen.getByText(/缺少 ct0/)).not.toBeNull();
  });

  it("成对的行不显示半行警告", () => {
    renderInputs(JSON.stringify([{ id: "cookie-0", authToken: "AAA", csrfToken: "CT0A" }]));
    expect(screen.queryByText(/缺少 ct0/)).toBeNull();
  });
});
