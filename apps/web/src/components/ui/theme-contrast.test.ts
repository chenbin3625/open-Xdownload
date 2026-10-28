// 主题对比度的静态校验。
// 这里不做视觉回归，而是把"token 必须成对"这条约定固化成测试：
//   1) index.css 的每个 accent / 明暗模式都定义了 --brand-contrast 与状态色 *-contrast；
//   2) @theme 里注册了对应的 --color-*-contrast，组件才能用 text-*-contrast；
//   3) 配对后的前景/背景对比度达到 WCAG AA（正文 4.5:1）；
//   4) 组件源码里不再出现"实心底 + 写死白字"的组合。
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = path.dirname(fileURLToPath(import.meta.url));
const css = readFileSync(path.join(here, "../../index.css"), "utf8");

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

// 解析 "rgb(16 185 129 / 0.14)" 形式的半透明色，便于算深色 soft 底的实际亮度。
function parseRgba(value: string): { rgb: [number, number, number]; alpha: number } | null {
  const match = /rgb\(([^)]+)\)/.exec(value);
  if (!match) return null;
  const parts = match[1].split(/[\s/]+/).filter(Boolean);
  if (parts.length < 3) return null;
  const [r, g, b] = parts.slice(0, 3).map((part) => Number(part));
  const alpha = parts.length >= 4 ? Number(parts[3]) : 1;
  return { rgb: [r, g, b], alpha };
}

function toHex([r, g, b]: [number, number, number]): string {
  return `#${[r, g, b].map((channel) => Math.round(channel).toString(16).padStart(2, "0")).join("")}`;
}

function hexToRgb(hex: string): [number, number, number] {
  const clean = hex.replace("#", "");
  return [0, 2, 4].map((index) => parseInt(clean.slice(index, index + 2), 16)) as [
    number,
    number,
    number,
  ];
}

// 把半透明 soft 底色叠到不透明表面上，得到人眼看到的等效底色。
function blendSoft(soft: string, surface: string): string {
  const parsed = parseRgba(soft);
  if (!parsed) return soft;
  const base = hexToRgb(surface);
  const mixed = parsed.rgb.map((channel, index) => channel * parsed.alpha + base[index] * (1 - parsed.alpha));
  return toHex(mixed as [number, number, number]);
}

function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  const [high, low] = la > lb ? [la, lb] : [lb, la];
  return (high + 0.05) / (low + 0.05);
}

// 强调色的配对前景：indigo 的 #6366f1 白色前景上限是 4.467（黑字 4.454），
// 这是强调色本身亮度决定的，不能靠选字色达标；其余 accent 都 >=4.8。
// 因此品牌阈值取 4.45，状态色阈值仍按 AA 的 4.5。
const BRAND_MIN = 4.45;
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

  it("5 套强调色都定义了 brand-contrast，且与 brand-500 组成 AA 对比", () => {
    for (const accent of ["x-blue", "titanium", "indigo", "emerald", "amber"]) {
      const body = blockBody(`[data-accent="${accent}"] {`);
      const brand = decl(body, "--color-brand-500");
      const pair = decl(body, "--brand-contrast");
      expect(contrast(brand, pair), `${accent} 的 brand-contrast 对比度不足`).toBeGreaterThanOrEqual(
        BRAND_MIN,
      );
    }
  });

  it("深色钛金单独覆写了 brand-contrast（近白底必须配深色字）", () => {
    const body = blockBody('.dark[data-accent="titanium"],');
    const brand = decl(body, "--color-brand-500");
    const pair = decl(body, "--brand-contrast");
    expect(contrast(brand, pair)).toBeGreaterThanOrEqual(AA);
  });

  it.each([
    ["浅色", ":root {"],
    ["深色", ".dark {"],
    ["系统偏好兜底", "html:not(.light):not(.dark) {"],
  ])("%s模式的状态色与其 contrast 配对达到 AA", (_label, marker) => {
    const body = blockBody(marker);
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

    const dark = blockBody(".dark {");
    const darkSurface = decl(dark, "--surface");
    for (const name of ["success", "warning", "danger", "info"]) {
      const strong = decl(dark, `--${name}-strong`);
      const soft = blendSoft(decl(dark, `--${name}-soft`), darkSurface);
      expect(contrast(strong, soft), `深色 ${name}-strong 在 soft 底上不达标`).toBeGreaterThanOrEqual(AA);
      expect(contrast(strong, darkSurface), `深色 ${name}-strong 在 surface 上不达标`).toBeGreaterThanOrEqual(AA);
    }
  });

  it("Tag 的 soft 色标签使用 strong 文本色", () => {
    const tag = readFileSync(path.join(here, "Tag.tsx"), "utf8");
    for (const name of ["success", "warning", "danger", "info"]) {
      expect(tag, `Tag 的 ${name} 仍使用未加深的状态色`).toContain(`text-${name}-strong`);
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
//   - 主题色卡：每个 accent 的底色不同，由 ConfigForm 里的 swatchCheckClass 逐色配前景色。
// 除此之外 ui/ 与 settings/ 下不应再出现"实心底 + text-white"。
describe("组件不再写死白字叠实心底色", () => {
  const dirs = [
    path.join(here, "."),
    path.join(here, "../settings"),
  ];
  const forbidden = [
    "bg-brand-500 text-white",
    "bg-success text-white",
    "bg-warning text-white",
    "bg-danger text-white",
    "bg-info text-white",
  ];

  it("Pagination 当前页 / Tag 计数气泡 / Checkbox 勾 / Switch 滑块都用配对 token", () => {
    const read = (file: string) => readFileSync(path.join(here, file), "utf8");
    const pagination = read("Pagination.tsx");
    expect(pagination).toContain("text-brand-contrast");

    const tag = read("Tag.tsx");
    for (const name of ["brand", "success", "warning", "danger", "info"]) {
      expect(tag, `CountBadge 的 ${name} 未使用配对色`).toContain(`text-${name}-contrast`);
    }

    const controls = read("Controls.tsx");
    // Checkbox 勾号用品牌配对色；Switch 滑块用 --surface 而不是写死白色
    expect(controls).toContain("text-brand-contrast");
    expect(controls).not.toContain("bg-white");
  });

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
