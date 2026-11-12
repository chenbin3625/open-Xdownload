import { afterEach, beforeEach } from "vitest";
import i18n from "../lib/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("zh-CN");
});

// 本项目没开 vitest globals，Testing Library 的自动清理不会生效，
// 这里显式挂一次。node 环境下 cleanup 无 DOM 可清，直接跳过。
afterEach(async () => {
  if (typeof document === "undefined") return;
  const { act, cleanup } = await import("@testing-library/react");
  const { Toast } = await import("@douyinfe/semi-ui");
  await act(async () => { Toast.destroyAll(); });
  cleanup();
});

// jsdom 不实现 ResizeObserver / DOMRect.fromRect，浮层定位依赖它们。
// 补一份最小桩，让浮层组件能在测试环境挂载；定位数值本身不参与断言。
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

if (typeof globalThis.DOMRect === "undefined") {
  globalThis.DOMRect = class {
    x = 0;
    y = 0;
    width = 0;
    height = 0;
    top = 0;
    right = 0;
    bottom = 0;
    left = 0;
    static fromRect() {
      return new globalThis.DOMRect();
    }
    toJSON() {
      return this;
    }
  } as unknown as typeof DOMRect;
}

if (typeof HTMLElement !== "undefined" && !HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {};
}

if (typeof HTMLCanvasElement !== "undefined") {
  HTMLCanvasElement.prototype.getContext = (() => ({
    fillRect() {}, clearRect() {}, getImageData: () => ({ data: new Uint8ClampedArray(4) }),
    putImageData() {}, createImageData: () => [], setTransform() {}, drawImage() {}, save() {},
    fillText() {}, restore() {}, beginPath() {}, moveTo() {}, lineTo() {}, closePath() {}, stroke() {},
    translate() {}, scale() {}, rotate() {}, arc() {}, fill() {}, transform() {}, rect() {}, clip() {},
    measureText: (text: string) => ({ width: text.length * 8 }),
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
}
if (typeof window !== "undefined" && !window.matchMedia) {
  window.matchMedia = ((media: string) => ({ media, matches: false, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => true })) as typeof window.matchMedia;
}

if (typeof document !== "undefined") {
  const { Toast } = await import("@douyinfe/semi-ui");
  Toast.defaultOpts.motion = false;
}
