import { afterEach } from "vitest";

// 本项目没开 vitest globals，Testing Library 的自动清理不会生效，
// 这里显式挂一次。node 环境下 cleanup 无 DOM 可清，直接跳过。
afterEach(async () => {
  if (typeof document === "undefined") return;
  const { cleanup } = await import("@testing-library/react");
  cleanup();
});

// jsdom 不实现 ResizeObserver / DOMRect.fromRect，而 Radix 的浮层定位
// （Popover/Select/Tooltip 的 popper 与 Arrow）依赖它们。
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
