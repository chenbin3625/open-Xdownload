import { describe, expect, it } from "vitest";
import {
  buildBatchDownloadItems,
  defaultArchiveScheduleName,
  displayDownloadTarget,
  parseTargets,
} from "./BatchDownloadLauncher";

describe("parseTargets", () => {
  it("splits mixed delimiters and drops empties", () => {
    expect(parseTargets("elonmusk, 123\n@foo，bar")).toEqual(["elonmusk", "123", "@foo", "bar"]);
  });
});
describe("buildBatchDownloadItems", () => {
  it("dedupes kind+input case-insensitively", () => {
    const items = buildBatchDownloadItems("ElonMusk\nelonmusk", "1\n1", "foo");
    expect(items).toEqual([
      { kind: "user", input: "ElonMusk", title: "用户 @ElonMusk" },
      { kind: "list", input: "1", title: "列表 1" },
      { kind: "following", input: "foo", title: "关注 @foo" },
    ]);
  });
});

describe("displayDownloadTarget", () => {
  it("keeps ids and @handles", () => {
    expect(displayDownloadTarget("123")).toBe("123");
    expect(displayDownloadTarget("@foo")).toBe("@foo");
    expect(displayDownloadTarget("foo")).toBe("@foo");
  });
});

describe("defaultArchiveScheduleName", () => {
  it("summarizes the first target", () => {
    expect(defaultArchiveScheduleName([])).toBe("批量归档计划");
    expect(defaultArchiveScheduleName(buildBatchDownloadItems("a", "", ""))).toBe("用户 a");
    expect(defaultArchiveScheduleName(buildBatchDownloadItems("a\nb", "", ""))).toBe("用户 a 等 2 个目标");
  });
});
