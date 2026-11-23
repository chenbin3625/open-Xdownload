/// <reference types="vitest/config" />
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { brotliCompressSync, constants as zlibConstants, gzipSync } from "node:zlib";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";
import webPackage from "./package.json" with { type: "json" };

const compressibleExt = new Set([
  ".js",
  ".mjs",
  ".css",
  ".html",
  ".svg",
  ".json",
  ".map",
  ".txt",
  ".xml",
  ".webmanifest",
]);

function walkFiles(dir: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    if (statSync(full).isDirectory()) {
      files.push(...walkFiles(full));
      continue;
    }
    files.push(full);
  }
  return files;
}

// 构建期写出 .br / .gz：Go 静态层优先读 sidecar，首个访客不再付 Brotli CPU。
function precompress(): Plugin {
  let outDir = "";
  let buildFailed = false;
  return {
    name: "precompress",
    apply: "build",
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir);
    },
    buildStart() {
      buildFailed = false;
    },
    buildEnd(err) {
      if (err) buildFailed = true;
    },
    closeBundle(err) {
      if (err || buildFailed || !outDir || !existsSync(outDir)) return;
      for (const file of walkFiles(outDir)) {
        const ext = path.extname(file).toLowerCase();
        if (!compressibleExt.has(ext)) continue;
        const source = readFileSync(file);
        const gzip = gzipSync(source, { level: 9 });
        // Gzip's OS byte varies across build hosts; 255 keeps embedded assets reproducible.
        gzip[9] = 255;
        writeFileSync(file + ".gz", gzip);
        writeFileSync(
          file + ".br",
          brotliCompressSync(source, {
            params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 11 },
          }),
        );
      }
    },
  };
}

// 日期选择器只在推文归档页动态加载，避免它的样式阻塞首屏 CSS。
function deferDatePickerStyles(): Plugin {
  return {
    name: "defer-date-picker-styles",
    apply: "build",
    enforce: "post",
    transformIndexHtml(html) {
      return html.replace(/[ \t]*<link rel="stylesheet"[^>]*href="[^"]*datePicker-[^"]+\.css"[^>]*>(?:\r?\n)?/g, "");
    },
  };
}

// 后端默认监听 127.0.0.1:8787；dev / preview 都把 /api 代理过去，
// 这样两种本地预览方式都能直接连真实后端（preview 之前只有静态壳，看不到数据）。
const API_PROXY_TARGET = "http://127.0.0.1:8787";

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(webPackage.version),
  },
  plugins: [tailwindcss(), react(), deferDatePickerStyles(), precompress()],
  build: {
    target: "es2022",
    // 保持现状不产出 sourcemap：发布物由 Go embed 进二进制，体积优先。
    // 需要排查生产问题时临时改为 true（或在 CLI 上加 --sourcemap）。
    sourcemap: false,
    modulePreload: {
      polyfill: false,
      // 日期筛选只在推文归档页使用；保留动态 import，避免入口 HTML 预加载这块大依赖。
      resolveDependencies(filename, deps, context) {
        if (context.hostType === "html") {
          return deps.filter(dep => !dep.includes("/datePicker-"));
        }
        return deps;
      },
    },
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("/react/") || id.includes("/react-dom/") || id.includes("/scheduler/")) return "react";
          if (id.includes("/@tanstack/")) return "query";
          if (id.includes("/lucide-react/")) return "icons";
          // 组件按页面入口自然分块；把整套 Semi 合成一个共享 chunk 会把设置/归档专用控件提前带入首屏。
          if (id.includes("/@douyinfe/")) return undefined;
          // 只把首屏必需的基础库合并；date-fns、dnd-kit、react-window 等跟着用到它们的页面/组件按需分块。
          if (/\/node_modules\/(i18next|react-i18next|tailwind-merge|clsx|classnames|prop-types|@babel\/runtime|use-sync-external-store|lodash|lodash-es)\//.test(id)) return "vendor";
          return undefined;
        },
      },
    },
  },
  // 监听地址由 npm script 决定（见 package.json）：
  //   npm run dev / preview        → 127.0.0.1（默认，仅本机）
  //   npm run dev:lan / preview:lan → 0.0.0.0（局域网调试用，确认网络可信）
  server: {
    port: 5173,
    proxy: {
      "/api": API_PROXY_TARGET,
    },
  },
  preview: {
    port: 4173,
    proxy: {
      "/api": API_PROXY_TARGET,
    },
  },
  test: {
    // 默认 node：纯逻辑测试最快。组件测试在文件顶部用
    // `// @vitest-environment jsdom` 单独切换，避免全局装载 DOM。
    environment: "node",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
});
