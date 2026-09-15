import { render, type RenderOptions } from "@testing-library/react";
import React from "react";
import { TooltipProvider } from "../components/ui/Overlay";

// Radix Tooltip 必须挂在 Provider 下（真实应用里由 main.tsx 挂一次）。
// 组件测试单独渲染子树时补上，避免每个用例自己包一层。
export function renderWithProviders(ui: React.ReactElement, options?: RenderOptions) {
  return render(<TooltipProvider>{ui}</TooltipProvider>, options);
}

export * from "@testing-library/react";
