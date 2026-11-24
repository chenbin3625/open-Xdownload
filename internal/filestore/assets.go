package filestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var assetTokens = regexp.MustCompile(`\{([^{}]+)\}`)
var windowsReservedAssetName = regexp.MustCompile(`^(COM|LPT)[1-9]$`)

// TemplateUserDir 只渲染资产目录模板里依赖 {kind} 与 {username} 的部分，用于把
// 归档用户的实体目录与媒体落盘目录对齐（默认 {kind}/{username} 即 image/<handle>、
// video/<handle>）。模板依赖推文级变量（{tweet_id}/{year}/{text} 等）时无法在仅有
// 用户信息的情况下确定目录，返回 ok=false，调用方应退回旧的归档目录布局。
func TemplateUserDir(rule config.AssetRule, kind config.AssetKind, screenName string) (string, bool) {
	if err := config.ValidateAssetTemplate(rule.DirectoryTemplate, false); err != nil {
		return "", false
	}
	supported := true
	directory := assetTokens.ReplaceAllStringFunc(rule.DirectoryTemplate, func(token string) string {
		switch token {
		case "{kind}":
			return safeAssetSegment(string(kind))
		case "{username}":
			return safeAssetSegment(screenName)
		default:
			supported = false
			return token
		}
	})
	if !supported {
		return "", false
	}
	if rule.SeparateFolder != nil && *rule.SeparateFolder && !strings.Contains(rule.DirectoryTemplate, "{kind}") {
		directory = path.Join(safeAssetSegment(string(kind)), directory)
	}
	parts := []string{}
	for _, part := range strings.Split(path.Join(rule.Prefix, directory), "/") {
		if part != "" && part != "." {
			parts = append(parts, safeAssetSegment(part))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	key := path.Join(parts...)
	if !validAssetKey(key) {
		return "", false
	}
	return key, true
}

func TemplatePath(rule config.AssetRule, kind config.AssetKind, tweet parser.TweetData, media parser.Media, index int, ext string) (string, error) {
	if err := config.ValidateAssetTemplate(rule.DirectoryTemplate, false); err != nil {
		return "", err
	}
	if err := config.ValidateAssetTemplate(rule.FilenameTemplate, true); err != nil {
		return "", err
	}
	cleanExt := strings.TrimPrefix(strings.TrimSpace(ext), ".")
	extSegment := ""
	if cleanExt != "" {
		extSegment = safeAssetSegment(cleanExt)
	}
	values := map[string]string{
		"author_id":    tweet.Author.ID,
		"username":     tweet.Author.ScreenName,
		"tweet_id":     tweet.ID,
		"media_id":     media.ID,
		"index":        strconv.Itoa(index + 1),
		"published_at": tweet.CreatedAt.UTC().Format("20060102T150405Z"),
		"year":         tweet.CreatedAt.UTC().Format("2006"),
		"month":        tweet.CreatedAt.UTC().Format("01"),
		"day":          tweet.CreatedAt.UTC().Format("02"),
		"text":         safeAssetSegment(truncateSegment(tweet.Text, 64)),
		"ext":          extSegment,
		"kind":         string(kind),
	}
	render := func(template string) string {
		return assetTokens.ReplaceAllStringFunc(template, func(token string) string {
			if token == "{text}" || token == "{ext}" {
				return values[token[1:len(token)-1]]
			}
			return safeAssetSegment(values[token[1:len(token)-1]])
		})
	}
	parts := []string{}
	directory := render(rule.DirectoryTemplate)
	if rule.SeparateFolder != nil && *rule.SeparateFolder && !strings.Contains(rule.DirectoryTemplate, "{kind}") {
		directory = path.Join(safeAssetSegment(string(kind)), directory)
	}
	for _, part := range strings.Split(path.Join(rule.Prefix, directory), "/") {
		if part != "" && part != "." {
			parts = append(parts, safeAssetSegment(part))
		}
	}
	// Budget text after all other fields so IDs and the extension survive truncation.
	values["text"] = ""
	if count := strings.Count(rule.FilenameTemplate, "{text}"); count > 0 {
		available := max(0, config.MaxFilenameLength-len(render(rule.FilenameTemplate))) / count
		values["text"] = truncateSegment(cleanAssetSegment(tweet.Text), available)
	}
	renderedName := render(rule.FilenameTemplate)
	if values["ext"] == "" && strings.HasSuffix(rule.FilenameTemplate, "_{ext}") {
		renderedName = strings.TrimSuffix(renderedName, "_")
	}
	name := cleanAssetSegment(renderedName)
	extension := ""
	if values["ext"] != "" {
		if dotExt := "." + values["ext"]; strings.HasSuffix(name, dotExt) {
			extension = dotExt
		} else if underExt := "_" + values["ext"]; strings.HasSuffix(name, underExt) {
			extension = underExt
		} else {
			extension = path.Ext(name)
		}
	} else if strings.HasSuffix(rule.FilenameTemplate, ".{ext}") {
		extension = path.Ext(name)
	}
	if len(name) > config.MaxFilenameLength {
		digest := sha256.Sum256([]byte(name))
		suffix := "_" + hex.EncodeToString(digest[:6])
		available := config.MaxFilenameLength - len(extension) - len(suffix)
		if available < 1 {
			return "", fmt.Errorf("文件扩展名过长")
		}
		name = truncateSegment(strings.TrimSuffix(name, extension), available) + suffix + extension
	}
	key := path.Join(append(parts, name)...)
	if len(key) > 1000 || !validAssetKey(key) {
		return "", fmt.Errorf("渲染后的资源路径无效或过长")
	}
	return key, nil
}

func truncateSegment(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
func safeAssetSegment(value string) string {
	return truncateSegment(cleanAssetSegment(value), 220)
}

func cleanAssetSegment(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		if unicode.Is(unicode.Cf, r) && r != '\u200d' {
			return -1
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimFunc(value, func(r rune) bool {
		return r == '.' || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	})
	if value == "" || value == "." || value == ".." {
		return "_"
	}
	base := strings.ToUpper(strings.TrimRightFunc(strings.SplitN(value, ".", 2)[0], unicode.IsSpace))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || windowsReservedAssetName.MatchString(base) {
		value = "_" + value
	}
	return value
}
func validAssetKey(key string) bool {
	return key != "" &&
		key != "." &&
		key != ".." &&
		!strings.ContainsRune(key, '\x00') &&
		!strings.HasPrefix(key, "/") &&
		!strings.Contains(key, "\\") &&
		path.Clean(key) == key &&
		!strings.HasPrefix(key, "../")
}

// s3ClientKey 只包含影响 minio 客户端构造的字段，凭据以 SHA-256 摘要入键以免在内存键中保留明文密钥。
type s3ClientKey struct {
	endpoint string
	region   string
	credHash [32]byte
	secure   bool
}

const maxS3Clients = 8

type s3ClientEntry struct {
	client   *minio.Client
	lastUsed uint64
}

var s3Clients = struct {
	sync.Mutex
	seq   uint64
	byKey map[s3ClientKey]*s3ClientEntry
}{byKey: map[s3ClientKey]*s3ClientEntry{}}

func S3Client(target config.StorageTarget) (*minio.Client, error) {
	u, err := url.Parse(target.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("无效的 S3 endpoint")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("无效的 S3 endpoint")
	}
	if httpx.BlockedHost(host) {
		return nil, fmt.Errorf("S3 endpoint 不能使用受限地址: %s", host)
	}
	access, secret := target.AccessKey, target.SecretKey
	if target.AccessKeyEnv != "" {
		if !config.IsAllowedS3CredentialEnvName(target.AccessKeyEnv) {
			return nil, fmt.Errorf("凭据环境变量名称无效（仅允许 OPEN_XDOWNLOAD_S3_ 前缀）")
		}
		access = os.Getenv(target.AccessKeyEnv)
	}
	if target.SecretKeyEnv != "" {
		if !config.IsAllowedS3CredentialEnvName(target.SecretKeyEnv) {
			return nil, fmt.Errorf("凭据环境变量名称无效（仅允许 OPEN_XDOWNLOAD_S3_ 前缀）")
		}
		secret = os.Getenv(target.SecretKeyEnv)
	}
	transport, err := httpx.Transport("")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(access))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(secret))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(target.SessionToken))
	var credHash [32]byte
	copy(credHash[:], h.Sum(nil))

	key := s3ClientKey{endpoint: u.Host, region: target.Region, credHash: credHash, secure: u.Scheme == "https"}
	s3Clients.Lock()
	defer s3Clients.Unlock()
	if entry, ok := s3Clients.byKey[key]; ok {
		s3Clients.seq++
		entry.lastUsed = s3Clients.seq
		return entry.client, nil
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(access, secret, target.SessionToken),
		Secure:    key.secure,
		Region:    target.Region,
		Transport: httpx.GuardedRoundTripper(transport),
	})
	if err != nil {
		return nil, err
	}
	if len(s3Clients.byKey) >= maxS3Clients {
		var oldestKey s3ClientKey
		var oldestSeq uint64
		first := true
		for k, entry := range s3Clients.byKey {
			if first || entry.lastUsed < oldestSeq {
				oldestKey = k
				oldestSeq = entry.lastUsed
				first = false
			}
		}
		if !first {
			delete(s3Clients.byKey, oldestKey)
		}
	}
	s3Clients.seq++
	s3Clients.byKey[key] = &s3ClientEntry{client: client, lastUsed: s3Clients.seq}
	return client, nil
}

func assetCollisionCandidate(key, safeID string, attempt int) string {
	if attempt == 0 {
		return key
	}
	extension := path.Ext(key)
	stemBudget := max(1, min(170, 230-len(extension)-len(safeID)-len(strconv.Itoa(attempt))-2))
	return path.Join(path.Dir(key), truncateSegment(strings.TrimSuffix(path.Base(key), extension), stemBudget)+"_"+safeID+"_"+strconv.Itoa(attempt)+extension)
}

func PutAsset(ctx context.Context, target config.StorageTarget, key, source, contentType, assetID string) (string, error) {
	return putAsset(ctx, target, key, source, contentType, assetID, false)
}

// PutAssetFromTemp 把调用方刚下载到临时目录的新文件发布为资产。当 source 位于本地/NAS
// 的同一个 target.Root 内时，直接通过 os.Root 原子硬链接/重命名零拷贝发布，避免重复复制
// 和二次 fsync；若处于不同目录或文件系统（EXDEV）则自动回退到标准复制路径。
func PutAssetFromTemp(ctx context.Context, target config.StorageTarget, key, source, contentType, assetID string) (string, error) {
	return putAsset(ctx, target, key, source, contentType, assetID, true)
}

func putAsset(ctx context.Context, target config.StorageTarget, key, source, contentType, assetID string, fromTemp bool) (string, error) {
	if !validAssetKey(key) {
		return "", fmt.Errorf("无效的资产 key")
	}
	safeID := truncateSegment(safeAssetSegment(assetID), 64)
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("资产源文件不是普通文件: %s", source)
	}
	if target.Type == "s3" {
		client, err := S3Client(target)
		if err != nil {
			return "", err
		}
		for attempt := 0; attempt < 10; attempt++ {
			candidate := assetCollisionCandidate(key, safeID, attempt)
			if !validAssetKey(candidate) {
				return "", fmt.Errorf("无效的资产 key")
			}
			if _, statErr := client.StatObject(ctx, target.Bucket, candidate, minio.StatObjectOptions{}); statErr == nil {
				continue
			} else if response := minio.ToErrorResponse(statErr); response.StatusCode != 404 && response.Code != "NoSuchKey" && response.Code != "NotFound" {
				return "", statErr
			}
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				return "", err
			}
			opts := minio.PutObjectOptions{ContentType: contentType}
			opts.SetMatchETagExcept("*")
			_, err = client.PutObject(ctx, target.Bucket, candidate, file, info.Size(), opts)
			if response := minio.ToErrorResponse(err); response.StatusCode == 412 || response.Code == "PreconditionFailed" || response.StatusCode == 409 || response.Code == "ConditionalRequestConflict" {
				continue
			}
			if err != nil {
				return "", err
			}
			return candidate, nil
		}
		return "", fmt.Errorf("资源命名冲突过多")
	}

	if !filepath.IsAbs(target.Root) {
		return "", fmt.Errorf("本地或 NAS 目标必须使用绝对目录")
	}
	if err := config.ValidateDownloadRoot(target.Root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(target.Root, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(target.Root)
	if err != nil {
		return "", err
	}
	defer root.Close()

	parentDir := path.Dir(key)
	if parentDir != "." && parentDir != "" {
		if err = root.MkdirAll(filepath.FromSlash(parentDir), 0o700); err != nil {
			return "", err
		}
	}

	// 若调用方把新下载的临时文件放在同一 target.Root 下（例如 .xdl-tmp），先尝试在 os.Root
	// 边界内直接 Link/Rename 零拷贝发布；跨盘或不可链接时再退回复制 + fsync。
	if fromTemp {
		if relSource, ok := relPathWithinRoot(target.Root, source); ok {
			if candidate, published, pubErr := publishRootFile(root, relSource, key, safeID); published || pubErr != nil {
				return candidate, pubErr
			}
		}
	}

	// 同一资产的所有冲突候选名位于相同父目录：先一次性写入并 fsync 临时文件，
	// 随后循环以不覆盖方式发布到唯一目标名，避免每次命名冲突都重复复制与 fsync 大文件。
	var nonceBytes [8]byte
	if _, randErr := rand.Read(nonceBytes[:]); randErr != nil {
		return "", randErr
	}
	temp := filepath.FromSlash(path.Join(parentDir, ".asset-"+safeID+"-"+hex.EncodeToString(nonceBytes[:])+".part"))
	out, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, &contextAssetReader{ctx: ctx, reader: file})
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = root.Remove(temp)
		return "", copyErr
	}
	defer func() { _ = root.Remove(temp) }()

	for attempt := 0; attempt < 10; attempt++ {
		candidate := assetCollisionCandidate(key, safeID, attempt)
		if !validAssetKey(candidate) {
			return "", fmt.Errorf("无效的资产 key")
		}
		targetRel := filepath.FromSlash(candidate)
		err = root.Link(temp, targetRel)
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.ENOTSUP) {
			reservation, reserveErr := root.OpenFile(targetRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if reserveErr != nil {
				err = reserveErr
			} else if closeErr := reservation.Close(); closeErr != nil {
				_ = root.Remove(targetRel)
				err = closeErr
			} else {
				err = root.Rename(temp, targetRel)
				if err != nil {
					_ = root.Remove(targetRel)
				}
			}
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("资源命名冲突过多")
}

func relPathWithinRoot(rootDir, source string) (string, bool) {
	cleanRoot := filepath.Clean(rootDir)
	cleanSource := filepath.Clean(source)
	rel, err := filepath.Rel(cleanRoot, cleanSource)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// publishRootFile 尝试将 root 内部的临时文件零拷贝发布为不覆盖的候选资产 key。
// 遇到 EXDEV 时返回 ( "", false, nil ) 交由调用方退回跨卷复制。
func publishRootFile(root *os.Root, relSource, key, safeID string) (string, bool, error) {
	for attempt := 0; attempt < 10; attempt++ {
		candidate := assetCollisionCandidate(key, safeID, attempt)
		if !validAssetKey(candidate) {
			return "", true, fmt.Errorf("无效的资产 key")
		}
		targetRel := filepath.FromSlash(candidate)
		err := root.Link(relSource, targetRel)
		if errors.Is(err, syscall.EXDEV) {
			return "", false, nil
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) {
			reservation, reserveErr := root.OpenFile(targetRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if reserveErr != nil {
				err = reserveErr
			} else if closeErr := reservation.Close(); closeErr != nil {
				_ = root.Remove(targetRel)
				err = closeErr
			} else {
				err = root.Rename(relSource, targetRel)
				if err != nil {
					_ = root.Remove(targetRel)
					if errors.Is(err, syscall.EXDEV) {
						return "", false, nil
					}
				}
			}
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", true, err
		}
		return candidate, true, nil
	}
	return "", true, fmt.Errorf("资源命名冲突过多")
}

type contextAssetReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextAssetReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func AssetHash(ctx context.Context, source string) (string, int64, error) {
	f, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, &contextAssetReader{ctx: ctx, reader: f})
	return hex.EncodeToString(h.Sum(nil)), size, err
}

// OpenAsset uses a directory handle so symlink races cannot escape local/NAS roots.
func OpenAsset(ctx context.Context, target config.StorageTarget, key string) (io.ReadSeekCloser, int64, error) {
	if !validAssetKey(key) {
		return nil, 0, fmt.Errorf("无效的资产 key")
	}
	if target.Type == "s3" {
		client, err := S3Client(target)
		if err != nil {
			return nil, 0, err
		}
		object, err := client.GetObject(ctx, target.Bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return nil, 0, err
		}
		stat, err := object.Stat()
		if err != nil {
			object.Close()
			return nil, 0, err
		}
		return object, stat.Size, nil
	}
	if !filepath.IsAbs(target.Root) {
		return nil, 0, fmt.Errorf("本地或 NAS 目标必须使用绝对目录")
	}
	if err := config.ValidateDownloadRoot(target.Root); err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(target.Root)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(key))
	if err != nil {
		return nil, 0, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		file.Close()
		return nil, 0, fmt.Errorf("资产不是有效的普通文件: %v", err)
	}
	return file, stat.Size(), nil
}
