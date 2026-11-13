package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
)

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetStorageDocument(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, d.Redacted())
}
func (s *Server) updateStorage(w http.ResponseWriter, r *http.Request) {
	var document config.StorageDocument
	if err := decodeJSON(w, r, &document); err != nil {
		writeError(w, 400, err)
		return
	}
	d, err := s.store.SaveStorageDocument(r.Context(), document)
	if err != nil {
		var validation *storage.UserFacingError
		if errors.As(err, &validation) {
			writeError(w, validation.Status, err)
		} else {
			writeError(w, 500, err)
		}
		return
	}
	writeJSON(w, 200, d.Redacted())
}
func (s *Server) previewStoragePath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Document  config.StorageDocument `json:"document"`
		Kind      config.AssetKind       `json:"kind"`
		Tweet     parser.TweetData       `json:"tweet"`
		Media     parser.Media           `json:"media"`
		Index     int                    `json:"index"`
		Extension string                 `json:"extension"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := req.Document.Validate(); err != nil {
		writeError(w, 400, err)
		return
	}
	rule, target, err := req.Document.Rule(req.Kind)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	key, err := filestore.TemplatePath(rule, req.Kind, req.Tweet, req.Media, req.Index, req.Extension)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	location := target.Root + "/" + key
	if target.Type == "s3" {
		location = "s3://" + target.Bucket + "/" + key
	}
	writeJSON(w, 200, map[string]string{"key": key, "location": location})
}
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	s.serveAssetID(w, r, chi.URLParam(r, "assetID"))
}
func (s *Server) serveAssetID(w http.ResponseWriter, r *http.Request, id string) {
	_ = s.serveAssetIDResult(w, r, id, true)
}

// serveAssetIDResult writes an asset response and reports whether bytes were
// served. Download records can retain a legacy local path, so callers that
// have a fallback must be able to try that path when the versioned asset is
// missing or temporarily unavailable.
func (s *Server) serveAssetIDResult(w http.ResponseWriter, r *http.Request, id string, writeErrors bool) bool {
	a, err := s.store.GetAsset(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if writeErrors {
				writeError(w, 404, fmt.Errorf("asset not found"))
			}
		} else {
			if writeErrors {
				writeError(w, 500, err)
			}
		}
		return false
	}
	d, err := s.store.GetStorageVersion(r.Context(), a.PolicyVersion)
	if err != nil {
		if writeErrors {
			writeError(w, 500, err)
		}
		return false
	}
	var target config.StorageTarget
	for _, t := range d.Targets {
		if t.ID == a.TargetID && t.Version == a.TargetVersion {
			target = t
			break
		}
	}
	if target.ID == "" {
		if writeErrors {
			writeError(w, 500, fmt.Errorf("asset target version not found"))
		}
		return false
	}
	file, size, err := filestore.OpenAsset(r.Context(), target, a.Key)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, os.ErrNotExist) || minio.ToErrorResponse(err).StatusCode == 404 {
			status = http.StatusNotFound
		}
		if writeErrors {
			writeError(w, status, fmt.Errorf("asset unavailable"))
		}
		return false
	}
	defer file.Close()
	if size != a.Size {
		if writeErrors {
			writeError(w, 409, fmt.Errorf("asset size changed"))
		}
		return false
	}
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("ETag", `"`+a.ContentHash+`"`)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", `inline`)
	http.ServeContent(w, r, path.Base(a.Key), a.CreatedAt, file)
	return true
}
func (s *Server) serveDownloadAsset(w http.ResponseWriter, r *http.Request, id int64, preview bool) bool {
	assets, err := s.store.DownloadAssets(r.Context(), id)
	if err != nil {
		writeError(w, 500, err)
		return true
	}
	kinds := []config.AssetKind{config.AssetImage, config.AssetVideo}
	if preview {
		// Derived thumbnails are no longer generated. An image can still be
		// previewed from its original asset; videos use the remote poster fallback.
		kinds = []config.AssetKind{config.AssetImage}
	}
	for _, kind := range kinds {
		if assetID := assets[kind]; assetID != "" {
			if s.serveAssetIDResult(w, r, assetID, false) {
				return true
			}
		}
	}
	if preview && assets[config.AssetImage] != "" {
		return s.serveAssetIDResult(w, r, assets[config.AssetImage], false)
	}
	return false
}
func (s *Server) enrichAssetURLs(r *http.Request, records []storage.DownloadRecord) {
	if len(records) == 0 {
		return
	}
	ids := make([]int64, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	assetsByID, err := s.store.DownloadAssetsBatch(r.Context(), ids)
	if err != nil {
		return
	}
	for i := range records {
		assets := assetsByID[records[i].ID]
		records[i].AssetURLs = map[config.AssetKind]string{}
		for kind := range assets {
			// Download/preview endpoints retain the legacy FilePath fallback when
			// an asset object was removed or its historical target is unavailable.
			if kind == config.AssetImage || kind == config.AssetVideo {
				records[i].AssetURLs[kind] = "/api/library/downloads/" + strconv.FormatInt(records[i].ID, 10) + "/file"
			}
		}
		for _, kind := range []config.AssetKind{config.AssetImage, config.AssetVideo} {
			if id := assets[kind]; id != "" {
				records[i].FileURL = "/api/library/downloads/" + strconv.FormatInt(records[i].ID, 10) + "/file"
				break
			}
		}
	}
}

func (s *Server) migrateStorage(w http.ResponseWriter, r *http.Request) {
	document, err := s.store.GetStorageDocument(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !document.Enabled {
		writeError(w, 400, fmt.Errorf("请先启用分类存储"))
		return
	}
	job, err := s.store.CreateJob(r.Context(), storage.JobKindAssetMigration, "historical-assets", "历史资源迁移")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	s.manager.Notify()
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "job.created", JobID: job.ID, Payload: job}))
	writeJSON(w, 201, job)
}
