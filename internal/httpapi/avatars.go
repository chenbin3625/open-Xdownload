package httpapi

import (
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) serveAvatar(w http.ResponseWriter, r *http.Request) {
	screenName := strings.TrimPrefix(strings.TrimSpace(chi.URLParam(r, "screenName")), "@")
	if screenName == "" {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetAvatarCache(r.Context(), screenName)
	if err != nil || entry == nil || entry.FilePath == "" {
		http.NotFound(w, r)
		return
	}
	resolved, ok := resolvePathWithinRoots(entry.FilePath, []string{s.store.AvatarDir()})
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	setMediaSecurityHeaders(w)
	http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), f)
}

// serveDownloadThumbnail 返回本地生成并缓存的 JPEG 缩略图（最大边 480px），
// 支持纯离线局域网浏览媒体库；非 JPEG/PNG 格式自动回退到已有预览或原文件端点。
func (s *Server) serveDownloadThumbnail(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, err := s.store.GetDownload(r.Context(), id)
	if err != nil || record == nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(record.FilePath))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		s.serveDownloadPreview(w, r)
		return
	}
	thumbDir := s.store.ThumbnailDir()
	thumbPath := filepath.Join(thumbDir, fmt.Sprintf("%d.jpg", id))
	if f, err := os.Open(thumbPath); err == nil {
		defer f.Close()
		if info, statErr := f.Stat(); statErr == nil && !info.IsDir() {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "private, max-age=86400")
			setMediaSecurityHeaders(w)
			http.ServeContent(w, r, filepath.Base(thumbPath), info.ModTime(), f)
			return
		}
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		s.serveDownloadFile(w, r)
		return
	}
	roots, _ := s.store.DownloadRoots(r.Context())
	if cfg.DownloadDir != "" {
		roots = append([]string{cfg.DownloadDir}, roots...)
	}
	srcPath, ok := resolvePathWithinRoots(record.FilePath, roots)
	if !ok {
		s.serveDownloadFile(w, r)
		return
	}
	srcFile, err := os.Open(srcPath)
	if err != nil {
		s.serveDownloadFile(w, r)
		return
	}
	img, _, decodeErr := image.Decode(srcFile)
	_ = srcFile.Close()
	if decodeErr != nil {
		s.serveDownloadFile(w, r)
		return
	}
	thumb := resizeDownMaxDim(img, 480)
	if err := os.MkdirAll(thumbDir, 0o700); err == nil {
		tmp := thumbPath + ".tmp"
		if out, createErr := os.Create(tmp); createErr == nil {
			if encErr := jpeg.Encode(out, thumb, &jpeg.Options{Quality: 80}); encErr == nil {
				_ = out.Close()
				_ = os.Rename(tmp, thumbPath)
			} else {
				_ = out.Close()
				_ = os.Remove(tmp)
			}
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	setMediaSecurityHeaders(w)
	_ = jpeg.Encode(w, thumb, &jpeg.Options{Quality: 80})
}

func resizeDownMaxDim(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim || w <= 0 || h <= 0 {
		return src
	}
	var dstW, dstH int
	if w >= h {
		dstW = maxDim
		dstH = max(1, h*maxDim/w)
	} else {
		dstH = maxDim
		dstW = max(1, w*maxDim/h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := b.Min.Y + y*h/dstH
		for x := 0; x < dstW; x++ {
			sx := b.Min.X + x*w/dstW
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
