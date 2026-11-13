package xtid

import (
	"encoding/base64"
	"errors"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	tagRE          = regexp.MustCompile(`(?is)<[a-zA-Z][^>]*>`)
	attrRE         = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)
	animationSVGRE = regexp.MustCompile(`(?is)<svg\b[^>]*\bid\s*=\s*["']loading-x-anim-\d+["'][^>]*>`)
	pathTagRE      = regexp.MustCompile(`(?is)<path\b[^>]*>`)

	// 旧版 webpack 构建直接引用；新版 x-web 也用它命名签名 chunk。
	indicesFileRE = regexp.MustCompile(`(?:\.{0,2}/)?[\w./-]*?\b(?:ondemand\.s|sign\.o)[\w.-]*\.js`)
	// 签名 JS 里的 (e[38],16) 形式索引；多个连续出现时 Python 只保留最后一个捕获。
	keyByteIndicesRE = regexp.MustCompile(`(?:\(\w\[(\d{1,2})\],\s*16\))+`)

	assetURLRE             = regexp.MustCompile(`https?://[\w.-]+/(?:x-web/[\w./-]+|responsive-web/client-web/[\w./-]+)\.js`)
	legacyMainRE           = regexp.MustCompile(`/client-web/main\.([^."']+)\.js`)
	legacyHashMapRE        = regexp.MustCompile(`(\d+):\s*["']([0-9a-f]{7}|[0-9a-f]{16})["']`)
	legacyAnyMapRE         = regexp.MustCompile(`(\d+):\s*["']([^"']+)["']`)
	legacyHashValueRE      = regexp.MustCompile(`^(?:[0-9a-f]{7}|[0-9a-f]{16})$`)
	staticImportRE         = regexp.MustCompile(`(?:from\s*|import\s*)["'](\.{1,2}/[^"']+\.js)["']`)
	dynamicImportRE        = regexp.MustCompile("import\\s*\\(\\s*[`\"'](\\.{1,2}/[^`\"']+\\.js)[`\"']\\s*\\)")
	dynamicDefaultImportRE = regexp.MustCompile("import\\s*\\(\\s*[`\"'](\\.{1,2}/[^`\"']+\\.js)[`\"']\\s*\\)\\s*\\.then\\s*\\([^()]*\\.default\\s*\\(\\s*\\)\\s*\\)")

	nonDigitRE = regexp.MustCompile(`[^0-9]+`)
)

const legacyScriptBase = "https://abs.twimg.com/responsive-web/client-web/"

// parseAttrs 解析一个 HTML 开始标签的属性，属性名统一小写。
func parseAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
		name := strings.ToLower(m[1])
		value := m[2]
		if value == "" {
			value = m[3]
		}
		if value == "" {
			value = m[4]
		}
		attrs[name] = stdhtml.UnescapeString(value)
	}
	return attrs
}

func parseVerificationKey(page []byte) ([]byte, error) {
	pageStr := string(page)
	for _, tag := range tagRE.FindAllString(pageStr, -1) {
		lower := strings.ToLower(tag)
		if !strings.HasPrefix(lower, "<meta") {
			continue
		}
		attrs := parseAttrs(tag)
		if attrs["name"] != "twitter-site-verification" {
			continue
		}
		value := strings.TrimSpace(attrs["content"])
		if value == "" {
			return nil, errors.New("首页的 twitter-site-verification meta 缺少 content")
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			// X 偶尔会省略 padding，这里宽松处理。
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
			if err != nil {
				return nil, fmt.Errorf("twitter-site-verification 不是合法 base64: %w", err)
			}
		}
		if len(decoded) == 0 {
			return nil, errors.New("twitter-site-verification meta 解码后为空")
		}
		return decoded, nil
	}
	return nil, errors.New("首页缺少 twitter-site-verification meta")
}

// parseAnimationPaths 返回首页中所有 loading-x-anim SVG 的第二个 path 的 d 属性。
// 选择第二个 path 与参考实现的 g:first-child path:nth-child(2) 一致。
func parseAnimationPaths(page []byte) ([]string, error) {
	pageStr := string(page)
	locs := animationSVGRE.FindAllStringIndex(pageStr, -1)
	paths := make([]string, 0, len(locs))
	for _, loc := range locs {
		rest := pageStr[loc[1]:]
		end := strings.Index(strings.ToLower(rest), "</svg>")
		if end < 0 {
			continue
		}
		block := rest[:end]
		tags := pathTagRE.FindAllString(block, -1)
		if len(tags) < 2 {
			continue
		}
		d := strings.TrimSpace(parseAttrs(tags[1])["d"])
		if d == "" {
			continue
		}
		paths = append(paths, d)
	}
	if len(paths) == 0 {
		return nil, errors.New("首页缺少 loading-x-anim 动画数据")
	}
	return paths, nil
}

// parseAnimationRows 复刻参考实现：
//
//	dat = els[idx][9:].split("C")
//	[list(map(float, re.sub(r"[^\d]+", " ", x).split())) for x in dat]
func parseAnimationRows(d string) ([][]float64, error) {
	if len(d) < 9 {
		return nil, fmt.Errorf("动画 path 数据过短: %q", d)
	}
	parts := strings.Split(d[9:], "C")
	rows := make([][]float64, 0, len(parts))
	for _, part := range parts {
		fields := strings.Fields(nonDigitRE.ReplaceAllString(part, " "))
		row := make([]float64, 0, len(fields))
		for _, field := range fields {
			v, err := strconv.ParseFloat(field, 64)
			if err != nil {
				return nil, fmt.Errorf("解析动画数值 %q 失败: %w", field, err)
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseKeyByteIndices(js []byte) ([]int, error) {
	matches := keyByteIndicesRE.FindAllSubmatch(js, -1)
	if len(matches) == 0 {
		return nil, errors.New("签名 JS 中未找到 key byte 索引")
	}
	indices := make([]int, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return nil, fmt.Errorf("解析 key byte 索引 %q 失败: %w", m[1], err)
		}
		indices = append(indices, v)
	}
	return indices, nil
}

func matchesKeyByteIndices(js []byte) bool {
	return keyByteIndicesRE.Match(js)
}

func matchesIndicesFile(rawURL string) bool {
	return indicesFileRE.MatchString(rawURL)
}

// extractScriptURLs 提取首页引用的脚本地址，兼容两种旧 chunk 映射格式：
// 7 位与 16 位十六进制 hash（twscrape #327/#328 的修复）。
func extractScriptURLs(page, baseURL string) []string {
	var urls []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		for _, existing := range urls {
			if existing == u {
				return
			}
		}
		urls = append(urls, u)
	}

	for _, u := range assetURLRE.FindAllString(page, -1) {
		add(u)
	}

	if m := legacyMainRE.FindStringSubmatch(page); m != nil {
		add(legacyScriptBase + "main." + m[1] + ".js")
	}

	type hashEntry struct {
		id   string
		hash string
	}
	var hashes []hashEntry
	seen := make(map[string]bool)
	for _, m := range legacyHashMapRE.FindAllStringSubmatch(page, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		hashes = append(hashes, hashEntry{id: m[1], hash: m[2]})
	}
	names := make(map[string]string)
	for _, m := range legacyAnyMapRE.FindAllStringSubmatch(page, -1) {
		if legacyHashValueRE.MatchString(m[2]) {
			continue
		}
		names[m[1]] = m[2]
	}
	for _, h := range hashes {
		name := names[h.id]
		if name == "" {
			name = h.id
		}
		add(legacyScriptBase + name + "." + h.hash + "a.js")
	}
	return urls
}

// chunkReferences 从脚本内容中提取相对 import，优先返回疑似签名 chunk：
// 当前 x-web 构建用 import("./sign.js").then(e => e.default()) 懒加载它。
func chunkReferences(body []byte, baseURL string) (priority, normal []string) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, nil
	}
	resolve := func(ref string) string {
		u, err := url.Parse(ref)
		if err != nil {
			return ""
		}
		return base.ResolveReference(u).String()
	}
	appendUnique := func(dst []string, rawURL string) []string {
		if rawURL == "" {
			return dst
		}
		for _, existing := range dst {
			if existing == rawURL {
				return dst
			}
		}
		return append(dst, rawURL)
	}

	text := string(body)
	for _, m := range dynamicDefaultImportRE.FindAllStringSubmatch(text, -1) {
		priority = appendUnique(priority, resolve(m[1]))
	}
	for _, m := range staticImportRE.FindAllStringSubmatch(text, -1) {
		normal = appendUnique(normal, resolve(m[1]))
	}
	for _, m := range dynamicImportRE.FindAllStringSubmatch(text, -1) {
		normal = appendUnique(normal, resolve(m[1]))
	}
	return priority, normal
}
