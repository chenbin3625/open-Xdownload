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
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
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
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
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
	return os.MkdirAll(dir, 0o755)
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
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return "", err
	}
	filePath := filepath.Join(s.root, probeFilename())
	if err := os.WriteFile(filePath, probePayload, 0o644); err != nil {
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
	if filename, ok := downloader.InferredFilename(mediaURL, filenameHint, options.MaxFilenameLength); ok {
		result, ok, err := s.existingResult(ctx, dir, filename)
		if err != nil {
			return downloader.Result{}, err
		}
		if ok {
			return result, nil
		}
	}
	response, err := d.Open(ctx, mediaURL, options)
	if err != nil {
		return downloader.Result{}, err
	}
	defer response.Body.Close()

	filename := downloader.Filename(mediaURL, filenameHint, response.Header.Get("Content-Type"), options.MaxFilenameLength)
	existing, ok, err := s.existingResult(ctx, dir, filename)
	if err != nil {
		return downloader.Result{}, err
	}
	if ok {
		return existing, nil
	}
	var result downloader.Result
	err = s.withShare(ctx, func(share *smb2.Share) error {
		dirRel := s.relativePath(dir)
		if err := share.MkdirAll(dirRel, 0o755); err != nil {
			return err
		}
		fileRel := joinSlash(dirRel, filename)
		filePath := s.logicalPath(fileRel)
		_, err := share.Stat(fileRel)
		if err == nil {
			result = downloader.Result{Path: filePath, Skipped: true}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		file, err := share.OpenFile(fileRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				result = downloader.Result{Path: filePath, Skipped: true}
				return nil
			}
			return err
		}
		bytes, err := io.Copy(file, response.Body)
		closeErr := file.Close()
		if err != nil {
			_ = share.Remove(fileRel)
			return err
		}
		if closeErr != nil {
			_ = share.Remove(fileRel)
			return closeErr
		}
		if !options.ModTime.IsZero() {
			_ = share.Chtimes(fileRel, time.Now(), options.ModTime)
		}
		result = downloader.Result{Path: filePath, Bytes: bytes}
		return nil
	})
	if err != nil {
		return downloader.Result{}, err
	}
	return result, nil
}

func (s smbStore) existingResult(ctx context.Context, dir string, filename string) (downloader.Result, bool, error) {
	var result downloader.Result
	var found bool
	err := s.withShare(ctx, func(share *smb2.Share) error {
		fileRel := joinSlash(s.relativePath(dir), filename)
		info, err := share.Stat(fileRel)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		found = true
		result = downloader.Result{Path: s.logicalPath(fileRel), Bytes: info.Size(), Skipped: true}
		return nil
	})
	return result, found, err
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

var (
	smbSessionsMu sync.Mutex
	smbSessions   = map[string]*smbSharedSession{}
)

func (s smbStore) sharedSession() *smbSharedSession {
	key := s.signature()
	smbSessionsMu.Lock()
	defer smbSessionsMu.Unlock()
	entry, ok := smbSessions[key]
	if !ok {
		entry = &smbSharedSession{cfg: s}
		smbSessions[key] = entry
	}
	return entry
}

func (s smbStore) signature() string {
	return fmt.Sprintf("%s|%d|%s|%s|%s|%s", s.host, s.port, s.share, s.domain, s.username, s.password)
}

func (e *smbSharedSession) withShare(ctx context.Context, fn func(*smb2.Share) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.share == nil {
		if err := e.connect(ctx); err != nil {
			return err
		}
	}
	err := fn(e.share.WithContext(ctx))
	if isSMBConnectionError(err) {
		// 连接已失效，丢弃会话，下次调用时自动重连。
		_ = e.share.Umount()
		_ = e.session.Logoff()
		e.share = nil
		e.session = nil
	}
	return err
}

func (e *smbSharedSession) connect(ctx context.Context) error {
	address := net.JoinHostPort(e.cfg.host, strconv.Itoa(e.cfg.port))
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
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

func (s smbStore) uniquePath(share *smb2.Share, dirRel string, filename string, maxFilenameLength int) (string, string, error) {
	for index := 0; ; index++ {
		candidateName := filename
		if index > 0 {
			candidateName = downloader.FilenameWithSuffix(filename, fmt.Sprintf("(%d)", index), maxFilenameLength)
		}
		candidateRel := joinSlash(dirRel, candidateName)
		_, err := share.Stat(candidateRel)
		if os.IsNotExist(err) {
			return candidateRel, s.logicalPath(candidateRel), nil
		}
		if err != nil {
			return "", "", err
		}
	}
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
		client:   &http.Client{Timeout: 10 * time.Minute},
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

// webdavDirs 缓存已确认存在的 WebDAV 目录，避免每次下载都对同一路径重复发 MKCOL。
var webdavDirs sync.Map // map[string]struct{}

func (s webDAVStore) MkdirAll(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	if _, ok := webdavDirs.Load(dir); ok {
		return nil
	}
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
	webdavDirs.Store(dir, struct{}{})
	return nil
}

func (s webDAVStore) Rename(ctx context.Context, oldPath string, newPath string) error {
	exists, err := s.exists(ctx, oldPath)
	if err != nil || !exists {
		return err
	}
	if err := s.MkdirAll(ctx, logicalDir(newPath)); err != nil {
		return err
	}
	request, err := s.newRequest(ctx, "MOVE", oldPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Destination", s.fullURLForLogical(newPath))
	request.Header.Set("Overwrite", "T")
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
	if filename, ok := downloader.InferredFilename(mediaURL, filenameHint, options.MaxFilenameLength); ok {
		result, ok, err := s.existingResult(ctx, dir, filename)
		if err != nil {
			return downloader.Result{}, err
		}
		if ok {
			return result, nil
		}
	}

	response, err := d.Open(ctx, mediaURL, options)
	if err != nil {
		return downloader.Result{}, err
	}
	filename := downloader.Filename(mediaURL, filenameHint, response.Header.Get("Content-Type"), options.MaxFilenameLength)
	filePath := s.Join(dir, filename)
	exists, err := s.exists(ctx, filePath)
	if err != nil {
		_ = response.Body.Close()
		return downloader.Result{}, err
	}
	if exists {
		_ = response.Body.Close()
		result, ok, err := s.existingResult(ctx, dir, filename)
		if err != nil {
			return downloader.Result{}, err
		}
		if ok {
			return result, nil
		}
		return downloader.Result{Path: filePath, Skipped: true}, nil
	}
	result, conflict, err := s.putNewMedia(ctx, response, filePath, options)
	if conflict {
		return downloader.Result{Path: filePath, Skipped: true}, nil
	}
	if err != nil {
		return downloader.Result{}, err
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

func (s webDAVStore) putNewMedia(ctx context.Context, response *http.Response, filePath string, options downloader.Options) (downloader.Result, bool, error) {
	counter := &countWriter{}
	request, err := s.newRequest(ctx, http.MethodPut, filePath, io.TeeReader(response.Body, counter))
	if err != nil {
		_ = response.Body.Close()
		return downloader.Result{}, false, err
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
		return downloader.Result{}, false, err
	}
	defer putResponse.Body.Close()
	if closeErr != nil {
		_ = s.delete(ctx, filePath)
		return downloader.Result{}, false, closeErr
	}
	if putResponse.StatusCode >= 200 && putResponse.StatusCode < 300 {
		return downloader.Result{Path: filePath, Bytes: counter.n}, false, nil
	}
	if putResponse.StatusCode == http.StatusConflict || putResponse.StatusCode == http.StatusPreconditionFailed {
		return downloader.Result{}, true, nil
	}
	_ = s.delete(ctx, filePath)
	return downloader.Result{}, false, fmt.Errorf("WebDAV PUT failed: HTTP %d", putResponse.StatusCode)
}

func (s webDAVStore) uniquePath(ctx context.Context, dir string, filename string, maxFilenameLength int) (string, error) {
	for index := 0; ; index++ {
		candidateName := filename
		if index > 0 {
			candidateName = downloader.FilenameWithSuffix(filename, fmt.Sprintf("(%d)", index), maxFilenameLength)
		}
		candidate := s.Join(dir, candidateName)
		exists, err := s.exists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
}

func (s webDAVStore) existingResult(ctx context.Context, dir string, filename string) (downloader.Result, bool, error) {
	filePath := s.Join(dir, filename)
	info, exists, err := s.stat(ctx, filePath)
	if err != nil || !exists {
		return downloader.Result{}, exists, err
	}
	return downloader.Result{Path: filePath, Bytes: info.Size(), Skipped: true}, true, nil
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
