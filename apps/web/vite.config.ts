/// <reference types="vitest/config" />
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { brotliCompressSync, constants as zlibConstants, gzipSync } from "node:zlib";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

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
  return {
    name: "precompress",
    apply: "build",
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      if (!outDir) return;
      for (const file of walkFiles(outDir)) {
        const ext = path.extname(file).toLowerCase();
        if (!compressibleExt.has(ext)) continue;
        const source = readFileSync(file);
        writeFileSync(file + ".gz", gzipSync(source, { level: 9 }));
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

export default defineConfig({
  plugins: [tailwindcss(), react(), precompress()],
  build: {
    modulePreload: {
      polyfill: false,
      resolveDependencies(_filename, deps) {
        // 入口只预加载壳层；页面 chunk 按需加载。
        if (!_filename.includes("index")) return deps;
        return deps.filter((dep) => !/(?:SettingsPage)/.test(dep));
      },
    },
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("/react/") || id.includes("/react-dom/") || id.includes("/scheduler/")) return "react";
          if (id.includes("/@tanstack/")) return "query";
          if (id.includes("/@ant-design/icons/")) return "icons";
          if (id.includes("/antd/")) return "antd";
          return "vendor";
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8787",
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
