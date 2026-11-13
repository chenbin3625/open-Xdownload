// 主题对比度的静态校验。
// 这里不做视觉回归，而是把"token 必须成对"这条约定固化成测试：
//   1) index.css 定义了 --brand-contrast 与状态色 *-contrast；
//   2) @theme 里注册了对应的 --color-*-contrast，组件才能用 text-*-contrast；
//   3) 配对后的前景/背景对比度达到 WCAG AA（正文 4.5:1）；
//   4) 组件源码里不再出现"实心底 + 写死白字"的组合。
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.join(here, "../..");

// index.css 只保留 @import；校验前把本地样式文件展开成一份完整的 CSS。
// 裸模块（tailwindcss）跳过，它不参与令牌断言。
function readCss(source: string, baseDir: string, seen = new Set<string>()): string {
  return source.replace(/@import\s+"([^"]+)";/g, (_match, spec: string) => {
    if (!spec.startsWith(".")) return "";
    const file = path.resolve(baseDir, spec);
    if (seen.has(file)) return "";
    seen.add(file);
    return readCss(readFileSync(file, "utf8"), path.dirname(file), seen);
  });
}

const css = readCss(readFileSync(path.join(root, "index.css"), "utf8"), root);

// 从 "marker {" 开始截到下一条 "}"，够用于本项目这种扁平声明块。
function blockBody(marker: string): string {
  const start = css.indexOf(marker);
  expect(start, `index.css 缺少 ${marker}`).toBeGreaterThanOrEqual(0);
  const open = css.indexOf("{", start);
  const close = css.indexOf("}", open);
  return css.slice(open + 1, close);
}

function decl(body: string, name: string): string {
  const match = new RegExp(`${name}\\s*:\\s*([^;]+);`).exec(body);
  expect(match, `缺少声明 ${name}`).not.toBeNull();
  return (match as RegExpExecArray)[1].trim();
}

function luminance(hex: string): number {
  const clean = hex.replace("#", "");
  const full =
    clean.length === 3
      ? clean
          .split("")
          .map((char) => char + char)
          .join("")
      : clean;
  const channels = [0, 2, 4].map((index) => parseInt(full.slice(index, index + 2), 16) / 255);
  const linear = channels.map((value) =>
    value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  const [high, low] = la > lb ? [la, lb] : [lb, la];
  return (high + 0.05) / (low + 0.05);
}

const AA = 4.5;

describe("主题对比度 token", () => {
  it("@theme 注册了 contrast 颜色工具类", () => {
    for (const name of ["brand", "success", "warning", "danger", "info"]) {
      expect(css).toContain(`--color-${name}-contrast: var(--${name}-contrast);`);
    }
    for (const name of ["success", "warning", "danger", "info"]) {
      expect(css).toContain(`--color-${name}-strong: var(--${name}-strong);`);
    }
  });

  it("固定品牌色与其前景色组成 AA 对比", () => {
    const theme = blockBody("@theme static {");
    const root = blockBody(":root {");
    expect(contrast(decl(theme, "--color-brand-500"), decl(root, "--brand-contrast"))).toBeGreaterThanOrEqual(AA);
  });

  it("实心主色与其前景色组成 AA 对比", () => {
    const theme = blockBody("@theme static {");
    const root = blockBody(":root {");
    expect(contrast(decl(theme, "--color-brand-600"), decl(root, "--accent-contrast"))).toBeGreaterThanOrEqual(AA);
  });

  it("品牌色阶与 Semi 主色同源，只有一套蓝", () => {
    const root = blockBody(":root {");
    for (const alias of ["--accent", "--accent-hover", "--accent-active"]) {
      expect(decl(root, alias)).toMatch(/^var\(--color-brand-\d+\)$/);
    }
    expect(css).toContain("--semi-color-primary: var(--accent);");
    expect(css).not.toMatch(/--semi-color-primary:\s*#[0-9a-f]{6}/i);
  });

  it("断点统一为约定值", () => {
    const maxWidths = new Set([...css.matchAll(/\(max-width:\s*([\d.]+)px\)/g)].map((m) => m[1]));
    expect([...maxWidths].sort()).toEqual(["1099.98", "359.98", "639.98", "767.98"]);
    const minWidths = new Set([...css.matchAll(/\(min-width:\s*([\d.]+)px\)/g)].map((m) => m[1]));
    expect([...minWidths].sort()).toEqual(["1280", "768"]);
  });

  it("业务样式只引用自有令牌，Semi 变量只出现在映射文件里", () => {
    const dir = path.join(root, "styles");
    for (const file of readdirSync(dir).filter((name) => name.endsWith(".css"))) {
      if (file === "semi.css") continue;
      const source = readFileSync(path.join(dir, file), "utf8");
      expect(source.includes("var(--semi-color-"), `${file} 仍在直接引用 Semi 变量`).toBe(false);
    }
  });

  it("固定浅色，不再根据系统偏好或旧主题属性切换配色", () => {
    expect(blockBody("html {")).toContain("color-scheme: light;");
    expect(css).not.toContain("prefers-color-scheme");
    expect(css).not.toContain(".dark");
    expect(css).not.toContain("data-accent");
  });

  it("状态色与其 contrast 配对达到 AA", () => {
    const body = blockBody(":root {");
    for (const name of ["success", "warning", "danger", "info"]) {
      const base = decl(body, `--${name}`);
      const pair = decl(body, `--${name}-contrast`);
      expect(contrast(base, pair), `${name} 的 contrast 对比度不足`).toBeGreaterThanOrEqual(AA);
    }
  });

  it("fg-subtle 在三种浅色表面上都达到 4.5:1，且比 fg-muted 更浅", () => {
    const body = blockBody(":root {");
    const subtle = decl(body, "--fg-subtle");
    const muted = decl(body, "--fg-muted");
    for (const surface of [decl(body, "--surface"), decl(body, "--canvas"), decl(body, "--surface-muted")]) {
      expect(contrast(subtle, surface), `fg-subtle 在 ${surface} 上不达标`).toBeGreaterThanOrEqual(AA);
    }
    expect(luminance(subtle), "fg-subtle 应比 fg-muted 更浅（更次要）").toBeGreaterThan(luminance(muted));
  });

  it("状态色的 strong 版本在 soft 底与普通表面上都达到 AA", () => {
    const light = blockBody(":root {");
    const lightSurface = decl(light, "--surface");
    for (const name of ["success", "warning", "danger", "info"]) {
      const strong = decl(light, `--${name}-strong`);
      const soft = decl(light, `--${name}-soft`);
      expect(contrast(strong, soft), `浅色 ${name}-strong 在 soft 底上不达标`).toBeGreaterThanOrEqual(AA);
      expect(contrast(strong, lightSurface), `浅色 ${name}-strong 在 surface 上不达标`).toBeGreaterThanOrEqual(AA);
    }
  });

  it("::selection 用配对前景色而不是写死白字", () => {
    const selection = blockBody("::selection {");
    expect(selection).toContain("color: var(--brand-contrast);");
    expect(selection).not.toContain("#ffffff");
  });
});

// 允许保留 text-white 的例外（已人工确认对比度）：
//   - 黑色/深色半透明遮罩上的白字（如 GalleryPage 的媒体覆盖层 bg-black/60、bg-black/75）；
// 除此之外 ui/ 与 settings/ 下不应再出现"实心底 + text-white"。
describe("组件不再写死白字叠实心底色", () => {
  const dirs = [
    path.join(here, "../semi"),
    path.join(here, "../settings"),
  ];
  const forbidden = [
    "bg-brand-500 text-white",
    "bg-success text-white",
    "bg-warning text-white",
    "bg-danger text-white",
    "bg-info text-white",
  ];

  it.each(dirs)("%s 下没有 '实心底 + text-white' 组合", (dir) => {
    const files = readdirSync(dir).filter((name) => name.endsWith(".tsx") && !name.includes(".test."));
    for (const file of files) {
      const source = readFileSync(path.join(dir, file), "utf8");
      for (const pattern of forbidden) {
        expect(source.includes(pattern), `${file} 仍在使用 ${pattern}`).toBe(false);
      }
    }
  });
});
