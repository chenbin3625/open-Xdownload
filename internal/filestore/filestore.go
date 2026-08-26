package filestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/hirochachacha/go-smb2"
)

type Store interface {
	Type() config.StorageType
	Root() string
	Join(base string, elems ...string) string
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, dir string) error
	Rename(ctx context.Context, oldPath string, newPath string) error
	SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error)
	SupportsLinks() bool
	TestWritable(ctx context.Context) (string, error)
}

func New(cfg config.AppConfig) (Store, error) {
	cfg = cfg.Normalized()
	if err := httpx.ValidateProxyURL(cfg.ProxyURL); err != nil {
		return nil, err
	}
	switch cfg.StorageType {
	case config.StorageSMB:
		if cfg.SMBHost == "" {
			return nil, fmt.Errorf("SMB 主机不能为空")
		}
		if cfg.SMBShare == "" {
			return nil, fmt.Errorf("SMB 共享名不能为空")
		}
		return newSMBStore(cfg), nil
	case config.StorageWebDAV:
		if cfg.WebDAVURL == "" {
			return nil, fmt.Errorf("WebDAV 地址不能为空")
		}
		parsed, err := url.Parse(cfg.WebDAVURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("WebDAV 地址无效")
		}
		return newWebDAVStore(cfg, parsed), nil
	default:
		root, err := filepath.Abs(cfg.DownloadDir)
		if err != nil {
			return nil, err
		}
		return localStore{root: root}, nil
	}
}

type localStore struct {
	root string
}

func (s localStore) Type() config.StorageType { return config.StorageLocal }
func (s localStore) Root() string             { return s.root }
func (s localStore) SupportsLinks() bool      { return true }

func (s localStore) Join(base string, elems ...string) string {
	items := append([]string{base}, elems...)
	return filepath.Join(items...)
}

func (s localStore) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (s localStore) MkdirAll(_ context.Context, dir string) error {
	// 本地目录收紧为 0700，与数据目录加固保持一致：私有账号下载的媒体
	// 不应被其他本地用户读取/遍历。MkdirAll 仅在创建目录时设置该模式。
	return os.MkdirAll(dir, 0o700)
}

func (s localStore) Rename(_ context.Context, oldPath string, newPath string) error {
	if _, err := os.Lstat(oldPath); os.IsNotExist(err) {
		return nil
	}
	return os.Rename(oldPath, newPath)
}

func (s localStore) SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error) {
	return d.DownloadWithOptions(ctx, mediaURL, dir, filenameHint, options)
}

func (s localStore) TestWritable(_ context.Context) (string, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", err
	}
	filePath := filepath.Join(s.root, probeFilename())
	if err := os.WriteFile(filePath, probePayload, 0o600); err != nil {
		return "", err
	}
	if err := os.Remove(filePath); err != nil {
		return filePath, err
	}
	return filePath, nil
}

type smbStore struct {
	host     string
	port     int
	share    string
	rootPath string
	root     string
	domain   string
	username string
	password string
}

func newSMBStore(cfg config.AppConfig) smbStore {
	rootPath := cleanSlashPath(cfg.SMBPath)
	root := fmt.Sprintf("smb://%s/%s", cfg.SMBHost, cfg.SMBShare)
	if rootPath != "" {
		root += "/" + rootPath
	}
	username := cfg.SMBUsername
	if username == "" {
		username = "guest"
	}
	return smbStore{
		host:     cfg.SMBHost,
		port:     cfg.SMBPort,
		share:    cfg.SMBShare,
		rootPath: rootPath,
		root:     root,
		domain:   cfg.SMBDomain,
		username: username,
		password: cfg.SMBPassword,
	}
}

func (s smbStore) Type() config.StorageType { return config.StorageSMB }
func (s smbStore) Root() string             { return s.root }
func (s smbStore) SupportsLinks() bool      { return false }

func (s smbStore) Join(base string, elems ...string) string {
	return joinURLPath(base, elems...)
}

func (s smbStore) Exists(ctx context.Context, path string) (bool, error) {
	err := s.withShare(ctx, func(share *smb2.Share) error {
		_, err := share.Stat(s.relativePath(path))
		return err
	})
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (s smbStore) MkdirAll(ctx context.Context, dir string) error {
	return s.withShare(ctx, func(share *smb2.Share) error {
		rel := s.relativePath(dir)
		if rel == "" {
			return nil
		}
		return share.MkdirAll(rel, 0o755)
	})
}

func (s smbStore) Rename(ctx context.Context, oldPath string, newPath string) error {
	return s.withShare(ctx, func(share *smb2.Share) error {
		oldRel := s.relativePath(oldPath)
		newRel := s.relativePath(newPath)
		if _, err := share.Stat(oldRel); os.IsNotExist(err) {
			return nil
		}
		if err := share.MkdirAll(path.Dir(newRel), 0o755); err != nil {
			return err
		}
		return share.Rename(oldRel, newRel)
	})
}

func (s smbStore) SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error) {
	response, err := d.Open(ctx, mediaURL, options)
	if err != nil {
		return downloader.Result{}, err
	}
	defer response.Body.Close()

	filename := downloader.Filename(mediaURL, filenameHint, response.Header.Get("Content-Type"), options.MaxFilenameLength)
	existing, complete, replaceExisting, err := s.existingFileState(ctx, dir, filename, response.ContentLength)
	if err != nil {
		return downloader.Result{}, err
	}
	if complete {
		return existing, nil
	}

	var result downloader.Result
	err = s.withShare(ctx, func(share *smb2.Share) error {
		dirRel := s.relativePath(dir)
		if err := share.MkdirAll(dirRel, 0o755); err != nil {
			return err
		}
		fileRel := joinSlash(dirRel, filename)
		// 原子写入：先写 .part 临时文件，Sync 后 rename 到最终路径，避免崩溃留下被
		// 当作完整下载的残缺文件。
		file, tempRel, err := createSMBTempFile(share, fileRel)
		if err != nil {
			return err
		}
		bytes, copyErr := io.Copy(file, response.Body)
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil {
			_ = file.Close()
			_ = share.Remove(tempRel)
			return copyErr
		}
		if syncErr != nil {
			_ = file.Close()
			_ = share.Remove(tempRel)
			return syncErr
		}
		if closeErr != nil {
			_ = share.Remove(tempRel)
			return closeErr
		}
		published, err := s.publishSMBTemp(share, tempRel, dirRel, filename, bytes, response.ContentLength, options.MaxFilenameLength, replaceExisting)
		if err != nil {
			return err
		}
		result = published
		if result.Skipped {
			return nil
		}
		if !options.ModTime.IsZero() {
			_ = share.Chtimes(s.relativePath(result.Path), time.Now(), options.ModTime)
		}
		return nil
	})
	if err != nil {
		return downloader.Result{}, err
	}
	return result, nil
}

// existingFileState reports whether dir/filename already holds a complete file.
// If the file existed before this download and its size mismatches
// contentLength, the caller may replace it as a stale partial. Files that appear
// later during this download are treated as filename conflicts instead.
func (s smbStore) existingFileState(ctx context.Context, dir string, filename string, contentLength int64) (downloader.Result, bool, bool, error) {
	var result downloader.Result
	var found bool
	var replace bool
	err := s.withShare(ctx, func(share *smb2.Share) error {
		fileRel := joinSlash(s.relativePath(dir), filename)
		state, err := s.smbFileState(share, fileRel, contentLength)
		if err != nil {
			return err
		}
		result = state.result
		found = state.complete
		replace = state.replace
		return nil
	})
	return result, found, replace, err
}

type smbFileState struct {
	result   downloader.Result
	complete bool
	replace  bool
}

func (s smbStore) smbFileState(share *smb2.Share, fileRel string, contentLength int64) (smbFileState, error) {
	info, err := share.Stat(fileRel)
	if os.IsNotExist(err) {
		return smbFileState{}, nil
	}
	if err != nil {
		return smbFileState{}, err
	}
	if info.IsDir() {
		return smbFileState{}, nil
	}
	if contentLength >= 0 && info.Size() != contentLength {
		return smbFileState{replace: true}, nil
	}
	return smbFileState{
		result:   downloader.Result{Path: s.logicalPath(fileRel), Bytes: info.Size(), Skipped: true},
		complete: true,
	}, nil
}

// smbTempStemMax 限制 SMB 临时文件基名的最大字节数：最终文件名可接近 NAME_MAX（如
// 240 字节），若把整个基名拼进临时名再加 UnixNano/计数/".part" 后缀，会超过 SMB
// 服务端的单个文件名上限触发 ENAMETOOLONG。临时名随后经 rename 发布到最终路径，
// 无需与最终文件名一致，故对基名截断即可。
const smbTempStemMax = 64

var smbTempCounter atomic.Uint64

// smbTempName 构造 SMB 临时文件相对路径：目录保持原样，基名截断到 smbTempStemMax
// 字节，再追加 UnixNano + 进程内计数器 + ".part"。UnixNano 提供跨进程唯一性，计数器
// 保证同进程内互不重复；碰撞（os.IsExist）由调用方循环重试。
func smbTempName(fileRel string) string {
	dirRel := path.Dir(fileRel)
	stem := truncateTempStem(path.Base(fileRel), smbTempStemMax)
	if stem == "" {
		stem = "media"
	}
	name := fmt.Sprintf("%s.%d.%d.part", stem, time.Now().UnixNano(), smbTempCounter.Add(1))
	return joinSlash(dirRel, name)
}

func createSMBTempFile(share *smb2.Share, fileRel string) (*smb2.File, string, error) {
	for {
		tempRel := smbTempName(fileRel)
		file, err := share.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return file, tempRel, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
}

// truncateTempStem 把 value 截断到最多 maxBytes 字节，且不截断多字节 UTF-8 字符。
func truncateTempStem(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

// maxFilenameConflicts bounds the suffix search in publishSMBTemp: if this many
// numbered variants already exist, we give up (and clean up the temp file)
// rather than loop unbounded on a pathologically full share.
const maxFilenameConflicts = 10000

func (s smbStore) publishSMBTemp(share *smb2.Share, tempRel string, dirRel string, filename string, bytes int64, contentLength int64, maxFilenameLength int, replaceBase bool) (downloader.Result, error) {
	triedReplaceBase := false
	for index := 0; index <= maxFilenameConflicts; index++ {
		candidateName := filename
		if index > 0 {
			candidateName = downloader.FilenameWithSuffix(filename, fmt.Sprintf("(%d)", index), maxFilenameLength)
		}
		candidateRel := joinSlash(dirRel, candidateName)
		if err := share.Rename(tempRel, candidateRel); err == nil {
			return downloader.Result{Path: s.logicalPath(candidateRel), Bytes: bytes}, nil
		} else if !s.smbRenameConflict(share, candidateRel, err) {
			_ = share.Remove(tempRel)
			return downloader.Result{}, err
		}
		if index == 0 && replaceBase && !triedReplaceBase {
			result, complete, removed, err := s.removeIncompleteSMBTarget(share, candidateRel, contentLength)
			if err != nil {
				_ = share.Remove(tempRel)
				return downloader.Result{}, err
			}
			if complete {
				_ = share.Remove(tempRel)
				return result, nil
			}
			triedReplaceBase = true
			if removed {
				index--
			}
		}
	}
	_ = share.Remove(tempRel)
	return downloader.Result{}, fmt.Errorf("too many filename conflicts publishing %s", filename)
}

func (s smbStore) smbRenameConflict(share *smb2.Share, candidateRel string, err error) bool {
	if os.IsExist(err) {
		return true
	}
	_, statErr := share.Stat(candidateRel)
	return statErr == nil
}

func (s smbStore) removeIncompleteSMBTarget(share *smb2.Share, fileRel string, contentLength int64) (downloader.Result, bool, bool, error) {
	state, err := s.smbFileState(share, fileRel, contentLength)
	if err != nil || state.complete || !state.replace {
		return state.result, state.complete, false, err
	}
	if err := share.Remove(fileRel); err != nil && !os.IsNotExist(err) {
		return downloader.Result{}, false, false, err
	}
	return downloader.Result{}, false, true, nil
}

func (s smbStore) TestWritable(ctx context.Context) (string, error) {
	var logicalPath string
	err := s.withShare(ctx, func(share *smb2.Share) error {
		dirRel := s.rootPath
		if dirRel != "" {
			if err := share.MkdirAll(dirRel, 0o755); err != nil {
				return err
			}
		}
		fileRel := joinSlash(dirRel, probeFilename())
		file, err := share.OpenFile(fileRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(probePayload)
		closeErr := file.Close()
		if writeErr != nil {
			_ = share.Remove(fileRel)
			return writeErr
		}
		if closeErr != nil {
			_ = share.Remove(fileRel)
			return closeErr
		}
		if err := share.Remove(fileRel); err != nil {
			return err
		}
		logicalPath = s.logicalPath(fileRel)
		return nil
	})
	if err != nil {
		return "", err
	}
	return logicalPath, nil
}

func (s smbStore) withShare(ctx context.Context, fn func(*smb2.Share) error) error {
	return s.sharedSession().withShare(ctx, fn)
}

// smbSharedSession 复用同一段 SMB 配置的会话，避免每个操作都重新 dial+mount。
// go-smb2 的单个 Share 不是并发安全的（conn.sequenceWindow 无锁自增），故用 mu 串行化。
type smbSharedSession struct {
	cfg     smbStore
	mu      sync.Mutex
	session *smb2.Session
	share   *smb2.Share
}

// maxSMBSessions bounds the shared-session cache so repeated SMB config changes
// (different host/share/credentials) don't leak unbounded TCP sessions. Reached
// only after many distinct configs; eviction then closes one idle session.
const maxSMBSessions = 8

var (
	smbSessionsMu sync.Mutex
	smbSessions   = map[string]*smbSharedSession{}
)

func (s smbStore) sharedSession() *smbSharedSession {
	key := s.signature()
	smbSessionsMu.Lock()
	defer smbSessionsMu.Unlock()
	if entry, ok := smbSessions[key]; ok {
		return entry
	}
	if len(smbSessions) >= maxSMBSessions {
		smbEvictIdleSession()
	}
	entry := &smbSharedSession{cfg: s}
	smbSessions[key] = entry
	return entry
}

// smbEvictIdleSession closes and removes one currently-idle session to make room
// when the cache is at capacity. Caller must hold smbSessionsMu. Only entries
// whose mutex is uncontended (TryLock succeeds) are evicted, so in-flight
// operations are never interrupted. If a caller obtained the entry just before
// eviction (lookup-then-use race), it will see share==nil and reconnect; withShare
// then re-registers that reconnected session. If another goroutine already
// re-registered a new entry for the same key, the reconnected session is orphaned
// (used for the current operation, then closed) so it cannot leak.
func smbEvictIdleSession() {
	for k, entry := range smbSessions {
		if !entry.mu.TryLock() {
			continue
		}
		entry.closeLocked()
		entry.mu.Unlock()
		delete(smbSessions, k)
		return
	}
}

// signature 用 \x00 分隔各字段作为共享会话的缓存键。\x00 不会出现在 SMB
// host/share/用户名/密码等字段中，避免管道符 "|" 分隔时字段内含 "|" 导致两个
// 不同配置碰撞到同一会话（进而以错误账户认证）。
func (s smbStore) signature() string {
	return strings.Join([]string{s.host, strconv.Itoa(s.port), s.share, s.domain, s.username, s.password}, "\x00")
}

func (e *smbSharedSession) withShare(ctx context.Context, fn func(*smb2.Share) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	orphaned := false
	if e.share == nil {
		if err := e.connect(ctx); err != nil {
			return err
		}
		// 重连后该会话可能已在 lookup-then-use 竞态中被逐出缓存；重新登记，避免重连
		// 出来的会话成为无人引用的孤儿（连接永不关闭）。锁序 e.mu -> smbSessionsMu
		// 不会死锁：smbSessionsMu 在别处只单独获取，或驱逐时以非阻塞 TryLock 获取 e.mu。
		// 若同键已被另一 goroutine 抢先登记了新条目，本会话不再被缓存引用（orphaned）：
		// 本次操作仍可安全使用刚建立的连接，但操作结束后必须关闭它，否则连接泄漏。
		orphaned = !e.reregisterLocked()
	}
	err := fn(e.share.WithContext(ctx))
	if isSMBConnectionError(err) || orphaned {
		// 连接已失效，丢弃会话；或本会话已成孤儿（未被缓存引用），操作完成后释放连接，
		// 避免泄漏 TCP/SMB 连接。两种情况下次调用时都会自动重连。
		e.closeLocked()
	}
	return err
}

// closeLocked 释放会话与挂载。调用方需持有 e.mu。
func (e *smbSharedSession) closeLocked() {
	if e.share != nil {
		_ = e.share.Umount()
	}
	if e.session != nil {
		_ = e.session.Logoff()
	}
	e.share = nil
	e.session = nil
}

// reregisterLocked 把本会话重新插入缓存（若不存在同键项）。调用方需持有 e.mu。
// 返回 true 表示调用方持有的会话就是缓存中该键的持有者（新插入，或原本即本会话）；
// 返回 false 表示同键已被另一条目占用，本会话已成为孤儿，调用方应在本轮操作结束后
// 关闭它，避免重连出来的会话无人引用而泄漏连接。
func (e *smbSharedSession) reregisterLocked() bool {
	key := e.cfg.signature()
	smbSessionsMu.Lock()
	defer smbSessionsMu.Unlock()
	if existing, ok := smbSessions[key]; ok {
		return existing == e
	}
	smbSessions[key] = e
	return true
}

func (e *smbSharedSession) connect(ctx context.Context) error {
	address := net.JoinHostPort(e.cfg.host, strconv.Itoa(e.cfg.port))
	// 带链路本地地址拨号防护（S2）：SMB 写入路径同样拒绝云元数据等地址。
	conn, err := httpx.DialGuarded(ctx, "tcp", address, 15*time.Second)
	if err != nil {
		return err
	}
	session, err := (&smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     e.cfg.username,
			Password: e.cfg.password,
			Domain:   e.cfg.domain,
		},
	}).DialContext(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return err
	}
	share, err := session.WithContext(ctx).Mount(e.cfg.share)
	if err != nil {
		_ = session.Logoff()
		return err
	}
	e.session = session
	e.share = share
	return nil
}

// isSMBConnectionError 判断是否为传输层错误（连接断开/响应损坏），
// 此类错误才需要丢弃会话；文件不存在、权限拒绝等逻辑错误不影响会话复用。
func isSMBConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var transportErr *smb2.TransportError
	if errors.As(err, &transportErr) {
		return true
	}
	var invalidErr *smb2.InvalidResponseError
	return errors.As(err, &invalidErr)
}

func (s smbStore) relativePath(value string) string {
	trimmed := strings.TrimPrefix(value, s.root)
	trimmed = cleanSlashPath(trimmed)
	return joinSlash(s.rootPath, trimmed)
}

func (s smbStore) logicalPath(rel string) string {
	rel = cleanSlashPath(rel)
	if s.rootPath != "" {
		rel = strings.TrimPrefix(rel, s.rootPath)
		rel = cleanSlashPath(rel)
	}
	return joinURLPath(s.root, rel)
}

type webDAVStore struct {
	base     *url.URL
	rootPath string
	root     string
	username string
	password string
	client   *http.Client
}

func newWebDAVStore(cfg config.AppConfig, base *url.URL) webDAVStore {
	rootPath := cleanSlashPath(cfg.WebDAVPath)
	return webDAVStore{
		base:     base,
		rootPath: rootPath,
		root:     webDAVURL(base, rootPath),
		username: cfg.WebDAVUsername,
		password: cfg.WebDAVPassword,
		// 复用共享 Transport：注入配置的代理（M1），并带链路本地地址拨号防护（S2）。
		client: httpx.Client(cfg.ProxyURL, 10*time.Minute),
	}
}

func (s webDAVStore) Type() config.StorageType { return config.StorageWebDAV }
func (s webDAVStore) Root() string             { return s.root }
func (s webDAVStore) SupportsLinks() bool      { return false }

func (s webDAVStore) Join(base string, elems ...string) string {
	return joinURLPath(base, elems...)
}

func (s webDAVStore) Exists(ctx context.Context, path string) (bool, error) {
	return s.exists(ctx, path)
}

// maxWebDAVDirs 限制 WebDAV 目录缓存大小，避免长运行进程中缓存无限增长。
const maxWebDAVDirs = 4096

// webdavDirs 缓存已确认存在的 WebDAV 目录，避免每次下载都对同一路径重复发 MKCOL。
// 缓存键包含存储身份（s.root），不同 WebDAV 配置之间不共享"已存在"判定。
var (
	webdavDirsMu sync.Mutex
	webdavDirs   = map[string]struct{}{}
)

// webDAVDirKey 用 \x00 拼接存储根与逻辑目录作为缓存键。\x00 不会出现在 URL 路径中，
// 确保不同 base URL/rootPath 的 WebDAV 存储即使传入相同目录字符串也不会互相命中缓存
// （否则切换服务器后 MkdirAll 会跳过 MKCOL，导致首次 PUT 409）。
func (s webDAVStore) webDAVDirKey(dir string) string {
	return s.root + "\x00" + dir
}

func (s webDAVStore) MkdirAll(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	key := s.webDAVDirKey(dir)
	webdavDirsMu.Lock()
	if _, ok := webdavDirs[key]; ok {
		webdavDirsMu.Unlock()
		return nil
	}
	webdavDirsMu.Unlock()
	rel := s.relativePath(dir)
	if rel == "" {
		return nil
	}
	parts := strings.Split(rel, "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = joinSlash(current, part)
		if err := s.mkcol(ctx, current); err != nil {
			return err
		}
	}
	webdavDirsMu.Lock()
	if len(webdavDirs) >= maxWebDAVDirs {
		for k := range webdavDirs {
			delete(webdavDirs, k)
			break
		}
	}
	webdavDirs[key] = struct{}{}
	webdavDirsMu.Unlock()
	return nil
}

// invalidateWebDAVDir 丢弃 dir 的"已存在"缓存，使下次 MkdirAll 重新确认并创建目录。
// 在 PUT 收到 409（父目录缺失）时调用——目录可能已被服务端删除而本地缓存仍认为存在。
func (s webDAVStore) invalidateWebDAVDir(dir string) {
	webdavDirsMu.Lock()
	delete(webdavDirs, s.webDAVDirKey(dir))
	webdavDirsMu.Unlock()
}

func (s webDAVStore) Rename(ctx context.Context, oldPath string, newPath string) error {
	exists, err := s.exists(ctx, oldPath)
	if err != nil || !exists {
		return err
	}
	if err := s.MkdirAll(ctx, logicalDir(newPath)); err != nil {
		return err
	}
	// 目标已存在时不覆盖（M4）：重命名沿用 Overwrite=F，避免静默覆盖同名目录/文件。
	if destExists, destErr := s.exists(ctx, newPath); destErr != nil {
		return destErr
	} else if destExists {
		return fmt.Errorf("WebDAV 重命名目标已存在: %s", newPath)
	}
	request, err := s.newRequest(ctx, "MOVE", oldPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Destination", s.fullURLForLogical(newPath))
	request.Header.Set("Overwrite", "F")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("WebDAV MOVE failed: HTTP %d", response.StatusCode)
}

func (s webDAVStore) SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error) {
	if err := s.MkdirAll(ctx, dir); err != nil {
		return downloader.Result{}, err
	}

	response, err := d.Open(ctx, mediaURL, options)
	if err != nil {
		return downloader.Result{}, err
	}
	filename := downloader.Filename(mediaURL, filenameHint, response.Header.Get("Content-Type"), options.MaxFilenameLength)
	filePath := s.Join(dir, filename)
	existing, complete, replaceExisting, err := s.existingFileState(ctx, dir, filename, response.ContentLength)
	if err != nil {
		_ = response.Body.Close()
		return downloader.Result{}, err
	}
	if complete {
		_ = response.Body.Close()
		return existing, nil
	}
	if replaceExisting {
		if err := s.delete(ctx, filePath); err != nil {
			_ = response.Body.Close()
			return downloader.Result{}, err
		}
	}
	result, conflictStatus, err := s.putNewMedia(ctx, response, filePath, options)
	if conflictStatus == http.StatusConflict {
		// 409=父目录缺失（webdavDirs 缓存陈旧，或目录被服务端删除）。失效缓存、重建目录
		// 后重新下载并重试一次；仍 409 则视为真正的错误而非"已下载"静默跳过。
		s.invalidateWebDAVDir(dir)
		if mkErr := s.MkdirAll(ctx, dir); mkErr != nil {
			return downloader.Result{}, mkErr
		}
		retry, openErr := d.Open(ctx, mediaURL, options)
		if openErr != nil {
			return downloader.Result{}, openErr
		}
		result, conflictStatus, err = s.putNewMedia(ctx, retry, filePath, options)
		if conflictStatus == http.StatusConflict {
			return downloader.Result{}, fmt.Errorf("WebDAV PUT failed: HTTP 409 (parent collection missing after retry)")
		}
	}
	if err != nil {
		return downloader.Result{}, err
	}
	if conflictStatus == http.StatusPreconditionFailed {
		// 412=文件已存在（真正的去重），跳过且不删除已有文件。
		result, complete, _, err := s.existingFileState(ctx, dir, filename, -1)
		if err != nil {
			return downloader.Result{}, err
		}
		if complete {
			return result, nil
		}
		return downloader.Result{Path: filePath, Skipped: true}, nil
	}
	return result, nil
}

func (s webDAVStore) TestWritable(ctx context.Context) (string, error) {
	if err := s.MkdirAll(ctx, s.Root()); err != nil {
		return "", err
	}
	filePath := s.Join(s.Root(), probeFilename())
	request, err := s.newRequest(ctx, http.MethodPut, filePath, bytes.NewReader(probePayload))
	if err != nil {
		return "", err
	}
	request.ContentLength = int64(len(probePayload))
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	request.Header.Set("If-None-Match", "*")
	response, err := s.client.Do(request)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return "", fmt.Errorf("WebDAV PUT failed: HTTP %d", response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		return filePath, err
	}
	if err := s.delete(ctx, filePath); err != nil {
		return filePath, err
	}
	return filePath, nil
}

// putNewMedia 上传媒体到 filePath。返回的 conflictStatus 为 409/412 时表示未上传成功
// 但不应删除已有文件：409=父目录缺失（可重试），412=文件已存在（真正去重，跳过）。
func (s webDAVStore) putNewMedia(ctx context.Context, response *http.Response, filePath string, options downloader.Options) (downloader.Result, int, error) {
	counter := &countWriter{}
	request, err := s.newRequest(ctx, http.MethodPut, filePath, io.TeeReader(response.Body, counter))
	if err != nil {
		_ = response.Body.Close()
		return downloader.Result{}, 0, err
	}
	if response.ContentLength >= 0 {
		request.ContentLength = response.ContentLength
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("If-None-Match", "*")
	if !options.ModTime.IsZero() {
		// X-OC-Mtime: Nextcloud/ownCloud 等 WebDAV 服务端会据此设置文件 mtime，
		// 不支持该扩展的服务端会忽略此头，无副作用且不增加额外请求。
		request.Header.Set("X-OC-Mtime", strconv.FormatInt(options.ModTime.Unix(), 10))
	}
	putResponse, err := s.client.Do(request)
	closeErr := response.Body.Close()
	if err != nil {
		_ = s.delete(ctx, filePath)
		return downloader.Result{}, 0, err
	}
	defer putResponse.Body.Close()
	if closeErr != nil {
		_ = s.delete(ctx, filePath)
		return downloader.Result{}, 0, closeErr
	}
	if putResponse.StatusCode >= 200 && putResponse.StatusCode < 300 {
		return downloader.Result{Path: filePath, Bytes: counter.n}, 0, nil
	}
	if putResponse.StatusCode == http.StatusConflict || putResponse.StatusCode == http.StatusPreconditionFailed {
		return downloader.Result{}, putResponse.StatusCode, nil
	}
	_ = s.delete(ctx, filePath)
	return downloader.Result{}, 0, fmt.Errorf("WebDAV PUT failed: HTTP %d", putResponse.StatusCode)
}

func (s webDAVStore) existingFileState(ctx context.Context, dir string, filename string, contentLength int64) (downloader.Result, bool, bool, error) {
	filePath := s.Join(dir, filename)
	info, exists, err := s.stat(ctx, filePath)
	if err != nil || !exists {
		return downloader.Result{}, false, false, err
	}
	if contentLength >= 0 && info.Size() >= 0 && info.Size() != contentLength {
		return downloader.Result{}, false, true, nil
	}
	// HEAD 未返回 Content-Length 时无法判断残缺；跳过删除，由 PUT 的 If-None-Match 去重。
	if contentLength < 0 || info.Size() < 0 {
		bytes := info.Size()
		if bytes < 0 {
			bytes = 0
		}
		return downloader.Result{Path: filePath, Bytes: bytes, Skipped: true}, true, false, nil
	}
	return downloader.Result{Path: filePath, Bytes: info.Size(), Skipped: true}, true, false, nil
}

type webDAVFileInfo struct {
	size int64
}

func (i webDAVFileInfo) Size() int64 {
	return i.size
}

func (s webDAVStore) stat(ctx context.Context, logicalPath string) (webDAVFileInfo, bool, error) {
	request, err := s.newRequest(ctx, http.MethodHead, logicalPath, nil)
	if err != nil {
		return webDAVFileInfo{}, false, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return webDAVFileInfo{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return webDAVFileInfo{}, false, nil
	}
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return webDAVFileInfo{size: response.ContentLength}, true, nil
	}
	return webDAVFileInfo{}, false, fmt.Errorf("WebDAV HEAD failed: HTTP %d", response.StatusCode)
}

func (s webDAVStore) exists(ctx context.Context, logicalPath string) (bool, error) {
	_, exists, err := s.stat(ctx, logicalPath)
	return exists, err
}

func (s webDAVStore) delete(ctx context.Context, logicalPath string) error {
	request, err := s.newRequest(ctx, http.MethodDelete, logicalPath, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("WebDAV DELETE failed: HTTP %d", response.StatusCode)
}

func (s webDAVStore) mkcol(ctx context.Context, rel string) error {
	request, err := s.newRequestForURL(ctx, "MKCOL", webDAVURL(s.base, rel), nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusMethodNotAllowed:
		return nil
	default:
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("WebDAV MKCOL failed: HTTP %d", response.StatusCode)
	}
}

func (s webDAVStore) newRequest(ctx context.Context, method string, logicalPath string, body io.Reader) (*http.Request, error) {
	return s.newRequestForURL(ctx, method, s.fullURLForLogical(logicalPath), body)
}

func (s webDAVStore) newRequestForURL(ctx context.Context, method string, targetURL string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return nil, err
	}
	if s.username != "" || s.password != "" {
		request.SetBasicAuth(s.username, s.password)
	}
	return request, nil
}

func (s webDAVStore) fullURLForLogical(logicalPath string) string {
	return webDAVURL(s.base, s.relativePath(logicalPath))
}

func (s webDAVStore) relativePath(value string) string {
	trimmed := strings.TrimPrefix(value, s.root)
	trimmed = cleanSlashPath(trimmed)
	return joinSlash(s.rootPath, trimmed)
}

func webDAVURL(base *url.URL, rel string) string {
	clone := *base
	basePath := strings.TrimRight(clone.Path, "/")
	rel = cleanSlashPath(rel)
	if rel == "" {
		if basePath == "" {
			clone.Path = "/"
		} else {
			clone.Path = basePath
		}
		return clone.String()
	}
	clone.Path = basePath + "/" + rel
	return clone.String()
}

func joinURLPath(base string, elems ...string) string {
	result := strings.TrimRight(base, "/")
	for _, elem := range elems {
		elem = cleanSlashPath(elem)
		if elem == "" {
			continue
		}
		if result == "" {
			result = elem
		} else {
			result += "/" + elem
		}
	}
	if result == "" {
		return base
	}
	return result
}

func joinSlash(items ...string) string {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		item = cleanSlashPath(item)
		if item != "" {
			cleaned = append(cleaned, item)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return path.Join(cleaned...)
}

func logicalDir(value string) string {
	value = strings.TrimRight(value, "/")
	index := strings.LastIndex(value, "/")
	if index <= 0 {
		return ""
	}
	return value[:index]
}

func cleanSlashPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.Trim(value, "/")
	return value
}

type countWriter struct {
	n int64
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

var probePayload = []byte("open-Xdownload storage test\n")

func probeFilename() string {
	return ".open-xdownload-storage-test-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".tmp"
}
