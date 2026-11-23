const tweetStatusPattern = /\/status\/\d+/;
const listLinkPattern = /(?:^|\/)(?:i\/)?lists\/\d+/;

// 顶部快捷输入框的分流：推文链接 → 单条推文，列表链接 → 列表，其余（含纯数字用户 ID）→ 用户。
export function detectQuickInputTab(input: string): "tweet_link" | "list" | "user" {
  const trimmed = input.trim();
  if (tweetStatusPattern.test(trimmed)) return "tweet_link";
  // 纯数字 ID 更常见的是用户 ID（列表 ID 也会走到这里，后端两种都能解析；
  // 之前的实现判成 list，导致粘贴用户 ID 后必须手动切 tab）。
  if (/^\d{5,}$/.test(trimmed)) return "user";
  if (listLinkPattern.test(trimmed)) return "list";
  return "user";
}
