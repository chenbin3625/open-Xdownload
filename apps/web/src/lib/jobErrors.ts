import type { Job } from "./api";

export interface AggregatedErrorGroup {
  id: string;
  reason: string;
  action?: string;
  targets: string[];
  count: number;
}

/**
 * 将任务错误字符串解析并聚合为结构化分组。
 * 兼容处理：
 * 1. 历史数据库中无换行粘连的长文本：`读取 @u1 的媒体时间线失败: ... 读取 @u2 ... 另有 23 个错误`
 * 2. 换行分隔的多条报错
 * 3. 后端新输出的聚合格式：`读取媒体时间线失败: ... (共 31 个账号: @u1、@u2 等)`
 * 4. 单一简单错误信息（如 404、网络超时）
 */
export function parseAggregatedErrors(errorText?: string): AggregatedErrorGroup[] {
  if (!errorText || !errorText.trim()) {
    return [];
  }

  const raw = errorText.trim();

  // 1. 先根据换行符切分行
  let rawLines: string[];
  if (raw.includes("\n")) {
    rawLines = raw.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  } else {
    rawLines = [raw];
  }

  // 2. 检查单行中是否包含未换行的多条历史记录拼接（如 "读取 @xxx"、"创建 @xxx"、"另有 X 个错误"）
  const chunks: string[] = [];
  const concatSplitRegex = /(?=(?:读取|创建)\s+@|另有\s*\d+\s*个错误)/g;

  for (const line of rawLines) {
    const parts = line.split(concatSplitRegex).map((p) => p.trim()).filter(Boolean);
    if (parts.length > 1) {
      chunks.push(...parts);
    } else {
      chunks.push(line);
    }
  }

  const groups: AggregatedErrorGroup[] = [];
  const groupMap = new Map<string, AggregatedErrorGroup>();
  let trailerCount = 0;

  for (const chunk of chunks) {
    // 检查尾部追加的 "另有 23 个错误"
    const trailerMatch = chunk.match(/^另有\s*(\d+)\s*个错误$/);
    if (trailerMatch) {
      trailerCount += parseInt(trailerMatch[1], 10) || 0;
      continue;
    }

    // 检查后端预聚合的格式：
    // e.g. "读取媒体时间线失败: X 客户端暂时全部限流，请稍后重试 (共 31 个账号: @user1、@user2 等)"
    const backendGroupMatch = chunk.match(
      /^(?:([^:：]+)[:：]\s*)?(.*?)\s*\((?:共\s*)?(\d+)\s*个(?:账号|目标|错误|次)?(?::|：)?\s*(.*?)\)$/,
    );
    if (backendGroupMatch && backendGroupMatch[3]) {
      const action = backendGroupMatch[1]?.trim();
      const reason = backendGroupMatch[2]?.trim() || chunk;
      const count = parseInt(backendGroupMatch[3], 10) || 1;
      const targetStr = backendGroupMatch[4] || "";
      const targets = targetStr.match(/@[a-zA-Z0-9_]+/g) || [];

      const key = `${action || ""}:::${reason}`;
      if (groupMap.has(key)) {
        const g = groupMap.get(key)!;
        g.count += count;
        for (const t of targets) {
          if (!g.targets.includes(t)) g.targets.push(t);
        }
      } else {
        const group: AggregatedErrorGroup = {
          id: `err-${groups.length + 1}`,
          reason,
          action: action || undefined,
          targets,
          count,
        };
        groupMap.set(key, group);
        groups.push(group);
      }
      continue;
    }

    // 单项错误行：
    // e.g. "读取 @alice 的媒体时间线失败: timeout"
    // e.g. "创建 @bob 的归档链接失败: permission denied"
    // e.g. "用户不存在或已被封禁 (404 Not Found)"
    let action: string | undefined;
    let target: string | undefined;
    let reason = chunk;

    const colonIdx = chunk.indexOf(": ") !== -1 ? chunk.indexOf(": ") : chunk.indexOf("：");
    if (colonIdx !== -1) {
      const prefix = chunk.slice(0, colonIdx).trim();
      reason = chunk.slice(colonIdx + (chunk[colonIdx] === "：" ? 1 : 2)).trim();

      const targetMatch = prefix.match(/@([a-zA-Z0-9_]+)/);
      if (targetMatch) {
        target = `@${targetMatch[1]}`;
        const cleanPrefix = prefix
          .replace(/@[a-zA-Z0-9_]+/g, "")
          .replace(/\s*的\s*/g, "")
          .replace(/\s+/g, "")
          .trim();
        action = cleanPrefix || undefined;
      } else if (prefix.length < 30) {
        action = prefix;
      }
    }

    const key = `${action || ""}:::${reason}`;
    if (groupMap.has(key)) {
      const g = groupMap.get(key)!;
      g.count += 1;
      if (target && !g.targets.includes(target)) {
        g.targets.push(target);
      }
    } else {
      const group: AggregatedErrorGroup = {
        id: `err-${groups.length + 1}`,
        reason,
        action: action || undefined,
        targets: target ? [target] : [],
        count: 1,
      };
      groupMap.set(key, group);
      groups.push(group);
    }
  }

  // 累加 trailer 计数（如 "另有 23 个错误"）
  if (trailerCount > 0) {
    if (groups.length > 0) {
      // 归入第一个（通常也是唯一的主原因）组
      groups[0].count += trailerCount;
    } else {
      groups.push({
        id: "err-1",
        reason: "其他未明确原因异常",
        targets: [],
        count: trailerCount,
      });
    }
  }

  return groups;
}

/**
 * 为任务列表"执行进度"列计算精简且语义明确的展示文案及悬停提示
 */
export function getJobProgressDisplay(job: Job): {
  displayMsg: string;
  tooltip: string;
  hasError: boolean;
} {
  const isFailed = job.status === "failed";
  const isPartial = job.status === "completed_with_errors";
  const hasError = isFailed || isPartial || Boolean(job.error);

  if (
    job.status === "pending" ||
    job.status === "downloading" ||
    job.status === "resolving"
  ) {
    const msg = job.message || "执行中...";
    return { displayMsg: msg, tooltip: msg, hasError: false };
  }

  if (job.status === "completed" && !job.error) {
    const msg = job.message || "已完成";
    return { displayMsg: msg, tooltip: msg, hasError: false };
  }

  if (job.status === "canceled") {
    const msg = job.message || "已取消";
    return { displayMsg: msg, tooltip: msg, hasError: false };
  }

  const groups = parseAggregatedErrors(job.error);

  // 无可解析的错误组
  if (groups.length === 0) {
    const msg = job.error || job.message || (isFailed ? "任务失败" : "执行中...");
    return { displayMsg: msg, tooltip: msg, hasError };
  }

  // 单一错误且仅针对单个目标/单次
  if (groups.length === 1 && groups[0].count <= 1 && groups[0].targets.length <= 1) {
    const msg = groups[0].reason || job.error || (isFailed ? "任务失败" : "部分异常");
    const tooltip = job.message && job.message !== msg ? `${msg}\n状态: ${job.message}` : msg;
    return { displayMsg: msg, tooltip, hasError };
  }

  // 多个目标或批量聚合错误
  const totalCount = groups.reduce((acc, g) => acc + g.count, 0);
  let displayMsg: string;
  if (groups.length === 1) {
    displayMsg = `${groups[0].reason} (${groups[0].count}项)`;
  } else {
    displayMsg = `部分失败 (${totalCount}项)`;
  }

  const tooltipLines: string[] = [];
  if (job.message) {
    tooltipLines.push(job.message);
  }
  for (const g of groups) {
    const targetsStr =
      g.targets.length > 0
        ? ` (${g.targets.slice(0, 5).join("、")}${g.targets.length > 5 ? " 等" : ""})`
        : "";
    tooltipLines.push(`• ${g.reason} [${g.count}项]${targetsStr}`);
  }

  return {
    displayMsg,
    tooltip: tooltipLines.join("\n"),
    hasError: true,
  };
}
