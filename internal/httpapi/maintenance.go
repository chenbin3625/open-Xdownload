package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func (s *Server) getMaintenanceStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStorageStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) scanMaintenance(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.ScanMaintenance(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) cleanupMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.store.RunMaintenanceCleanup(r.Context(), req.Action)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	tmpFile, err := os.CreateTemp(s.store.DataDir(), "open-xdownload-backup-*.db")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := s.store.CreateOnlineBackup(r.Context(), tmpPath); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	filename := fmt.Sprintf("open-xdownload-backup-%s.db", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/x-sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = io.Copy(w, f)
}

type configExportBundle struct {
	Version              int                       `json:"version"`
	ExportedAt           time.Time                 `json:"exportedAt"`
	ArchiveFilter        *config.ArchiveFilter     `json:"archiveFilter,omitempty"`
	WriteMetadataSidecar bool                      `json:"writeMetadataSidecar"`
	OfflineThumbnails    bool                      `json:"offlineThumbnails"`
	Schedules            []storage.ArchiveSchedule `json:"schedules"`
	Tags                 []storage.Tag             `json:"tags"`
	Collections          []storage.Collection      `json:"collections"`
}

func (s *Server) exportConfigBundle(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	schedules, err := s.store.ListArchiveSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tags, _ := s.store.ListTags(r.Context())
	collections, _ := s.store.ListCollections(r.Context())
	bundle := configExportBundle{
		Version:              1,
		ExportedAt:           time.Now().UTC(),
		ArchiveFilter:        cfg.ArchiveFilter,
		WriteMetadataSidecar: cfg.WriteMetadataSidecar,
		OfflineThumbnails:    cfg.OfflineThumbnails,
		Schedules:            schedules,
		Tags:                 tags,
		Collections:          collections,
	}
	filename := fmt.Sprintf("open-xdownload-config-%s.json", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(bundle)
}

func (s *Server) importConfigBundle(w http.ResponseWriter, r *http.Request) {
	var bundle configExportBundle
	if err := decodeJSON(w, r, &bundle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	createdSchedules, createdTags, createdCollections := 0, 0, 0
	for _, sc := range bundle.Schedules {
		sc.ID = 0
		if _, err := s.store.CreateArchiveSchedule(r.Context(), sc); err == nil {
			createdSchedules++
		}
	}
	for _, tg := range bundle.Tags {
		if _, err := s.store.CreateTag(r.Context(), tg.Name, tg.Color); err == nil {
			createdTags++
		}
	}
	for _, col := range bundle.Collections {
		if _, err := s.store.CreateCollection(r.Context(), col.Name, col.Description); err == nil {
			createdCollections++
		}
	}
	if bundle.ArchiveFilter != nil || bundle.WriteMetadataSidecar || bundle.OfflineThumbnails {
		if stored, err := s.store.GetStoredConfig(r.Context()); err == nil {
			if bundle.ArchiveFilter != nil {
				stored.ArchiveFilter = bundle.ArchiveFilter
			}
			stored.WriteMetadataSidecar = bundle.WriteMetadataSidecar
			stored.OfflineThumbnails = bundle.OfflineThumbnails
			_, _ = s.store.UpdateConfig(r.Context(), stored)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"createdSchedules":   createdSchedules,
		"createdTags":        createdTags,
		"createdCollections": createdCollections,
		"backupDir":          filepath.Base(s.store.DataDir()),
	})
}
