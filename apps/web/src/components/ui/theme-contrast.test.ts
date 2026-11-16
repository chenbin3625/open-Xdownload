import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const stylesDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../styles");
const sources = readdirSync(stylesDir)
  .filter(name => name.endsWith(".css"))
  .map(name => ({ name, css: readFileSync(path.join(stylesDir, name), "utf8") }));

describe("Semi 默认主题", () => {
  it.each(sources)("$name 不覆盖 Semi 官方主题变量", ({ css }) => {
    expect(css).not.toMatch(/--semi-[\w-]+\s*:/);
  });

  it("业务颜色和圆角引用 Semi 变量，不再维护独立色板", () => {
    const tokens = sources.find(source => source.name === "tokens.css")!.css;
    expect(tokens).not.toMatch(/#[\da-f]{3,8}\b|\brgba?\(/i);
    expect(tokens).toContain("--color-brand-500: var(--semi-color-primary);");
    expect(tokens).toContain("--color-danger: var(--semi-color-danger);");
    expect(tokens).toContain("--radius-card: var(--semi-border-radius-medium);");
    // Semi 的语义变量定义在 body 上，别名也必须在这个作用域解析。
    expect(tokens).toContain("body {");
    expect(tokens).not.toContain(":root {");
  });

  it("保留窄屏布局断点", () => {
    const css = sources.map(source => source.css).join("\n");
    const maxWidths = new Set([...css.matchAll(/\(max-width:\s*([\d.]+)px\)/g)].map(match => match[1]));
    expect([...maxWidths].sort()).toEqual(["1099.98", "1279.98", "359.98", "639.98", "767.98"]);
    const minWidths = new Set([...css.matchAll(/\(min-width:\s*([\d.]+)px\)/g)].map(match => match[1]));
    expect([...minWidths].sort()).toEqual(["1280", "768"]);
  });
});
