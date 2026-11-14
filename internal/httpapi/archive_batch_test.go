package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestCreateJobPersistsBatchSize(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	request := newLoopbackRequest(http.MethodPost, "/api/jobs",
		strings.NewReader(`{"kind":"following","input":"me","archiveMode":"tweets","batchSize":25}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var job storage.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.BatchSize != 25 {
		t.Fatalf("batchSize = %d, want 25", job.BatchSize)
	}
}

func TestCreateJobRejectsBatchSizeOverLimit(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	request := newLoopbackRequest(http.MethodPost, "/api/jobs",
		strings.NewReader(`{"kind":"following","input":"me","archiveMode":"tweets","batchSize":1001}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "每轮用户数") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateArchiveSchedulePersistsBatchSize(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	body := `{"name":"weekly","enabled":true,"intervalMinutes":10080,"items":[{"kind":"following","input":"me","archiveMode":"tweets","batchSize":50}]}`
	request := newLoopbackRequest(http.MethodPost, "/api/archive-schedules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var schedule storage.ArchiveSchedule
	if err := json.Unmarshal(response.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	if len(schedule.Items) != 1 || schedule.Items[0].BatchSize != 50 {
		t.Fatalf("items = %+v, want batchSize 50", schedule.Items)
	}
}
