package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var assetTokens = regexp.MustCompile(`\{([^{}]+)\}`)
var windowsReservedAssetName = regexp.MustCompile(`^(COM|LPT)[1-9]$`)
var s3AssetWrites [64]sync.Mutex

func TemplatePath(rule config.AssetRule, kind config.AssetKind, tweet parser.TweetData, media parser.Media, index int, ext string) (string, error) {
	if err := config.ValidateAssetTemplate(rule.DirectoryTemplate, false); err != nil {
		return "", err
	}
	if err := config.ValidateAssetTemplate(rule.FilenameTemplate, true); err != nil {
		return "", err
	}
	values := map[string]string{"author_id": tweet.Author.ID, "username": tweet.Author.ScreenName, "tweet_id": tweet.ID, "media_id": media.ID, "index": strconv.Itoa(index + 1), "published_at": tweet.CreatedAt.UTC().Format("20060102T150405Z"), "year": tweet.CreatedAt.UTC().Format("2006"), "month": tweet.CreatedAt.UTC().Format("01"), "day": tweet.CreatedAt.UTC().Format("02"), "text": safeAssetSegment(truncateSegment(tweet.Text, 64)), "ext": strings.TrimPrefix(ext, "."), "kind": string(kind)}
	render := func(template string) string {
		return assetTokens.ReplaceAllStringFunc(template, func(token string) string {
			if token == "{text}" {
				return values["text"]
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
	name := cleanAssetSegment(render(rule.FilenameTemplate))
	extension := path.Ext(name)
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
		return r
	}, value)
	value = strings.TrimSpace(strings.Trim(value, ". "))
	if value == "" || value == "." || value == ".." {
		return "_"
	}
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || windowsReservedAssetName.MatchString(base) {
		value = "_" + value
	}
	return value
}
func validAssetKey(key string) bool {
	return key != "" && !strings.HasPrefix(key, "/") && !strings.Contains(key, "\\") && path.Clean(key) == key && key != ".." && !strings.HasPrefix(key, "../")
}

// s3ClientKey 只包含影响 minio 客户端构造的字段。批量资产迁移会反复用同一 target
// 调用 PutAsset/OpenAsset，缓存客户端可避免重复解析 endpoint/读取环境变量；底层
// http.DefaultTransport 也因此在进程内共享连接池。
type s3ClientKey struct {
	endpoint     string
	region       string
	access       string
	secret       string
	sessionToken string
	secure       bool
}

var s3Clients = struct {
	sync.Mutex
	byKey map[s3ClientKey]*minio.Client
}{byKey: map[s3ClientKey]*minio.Client{}}

func S3Client(target config.StorageTarget) (*minio.Client, error) {
	u, err := url.Parse(target.Endpoint)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("无效的 S3 endpoint")
	}
	access, secret := target.AccessKey, target.SecretKey
	if target.AccessKeyEnv != "" {
		access = os.Getenv(target.AccessKeyEnv)
	}
	if target.SecretKeyEnv != "" {
		secret = os.Getenv(target.SecretKeyEnv)
	}
	key := s3ClientKey{endpoint: u.Host, region: target.Region, access: access, secret: secret, sessionToken: target.SessionToken, secure: u.Scheme == "https"}
	s3Clients.Lock()
	defer s3Clients.Unlock()
	if client, ok := s3Clients.byKey[key]; ok {
		return client, nil
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(access, secret, target.SessionToken), Secure: key.secure, Region: target.Region, Transport: http.DefaultTransport})
	if err != nil {
		return nil, err
	}
	s3Clients.byKey[key] = client
	return client, nil
}

func PutAsset(ctx context.Context, target config.StorageTarget, key, source, contentType, assetID string) (string, error) {
	if !validAssetKey(key) {
		return "", fmt.Errorf("无效的资产 key")
	}
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	var client *minio.Client
	var root *os.Root
	if target.Type == "s3" {
		digest := sha256.Sum256([]byte(target.Endpoint + "\x00" + target.Bucket + "\x00" + key))
		lock := &s3AssetWrites[int(digest[0])%len(s3AssetWrites)]
		lock.Lock()
		defer lock.Unlock()
		client, err = S3Client(target)
	} else {
		if err = config.ValidateDownloadRoot(target.Root); err == nil {
			err = os.MkdirAll(target.Root, 0o700)
		}
		if err == nil {
			root, err = os.OpenRoot(target.Root)
		}
		if root != nil {
			defer root.Close()
		}
	}
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 10; attempt++ {
		candidate := key
		if attempt > 0 {
			extension := path.Ext(key)
			candidate = path.Join(path.Dir(key), truncateSegment(strings.TrimSuffix(path.Base(key), extension), 170)+"_"+assetID+"_"+strconv.Itoa(attempt)+extension)
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if client != nil {
			if _, statErr := client.StatObject(ctx, target.Bucket, candidate, minio.StatObjectOptions{}); statErr == nil {
				continue
			} else if response := minio.ToErrorResponse(statErr); response.StatusCode != 404 && response.Code != "NoSuchKey" && response.Code != "NotFound" {
				return "", statErr
			}
			opts := minio.PutObjectOptions{ContentType: contentType}
			opts.SetMatchETagExcept("*")
			_, err = client.PutObject(ctx, target.Bucket, candidate, file, info.Size(), opts)
			if response := minio.ToErrorResponse(err); response.StatusCode == 412 || response.Code == "PreconditionFailed" {
				continue
			}
			return candidate, err
		}
		if err = root.MkdirAll(filepath.FromSlash(path.Dir(candidate)), 0o700); err != nil {
			return "", err
		}
		temp := filepath.FromSlash(path.Join(path.Dir(candidate), ".asset-"+assetID+".part"))
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
		err = root.Link(temp, filepath.FromSlash(candidate))
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EXDEV) {
			reservation, reserveErr := root.OpenFile(filepath.FromSlash(candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if reserveErr != nil {
				err = reserveErr
			} else {
				owned, _ := reservation.Stat()
				_, err = file.Seek(0, io.SeekStart)
				if err == nil {
					_, err = io.Copy(reservation, &contextAssetReader{ctx: ctx, reader: file})
				}
				if err == nil {
					err = reservation.Sync()
				}
				closeErr := reservation.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					if current, statErr := root.Lstat(filepath.FromSlash(candidate)); statErr == nil && os.SameFile(owned, current) {
						_ = root.Remove(filepath.FromSlash(candidate))
					}
				}
			}
		}
		_ = root.Remove(temp)
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
