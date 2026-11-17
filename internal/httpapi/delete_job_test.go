package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func newDeleteJobTestServer(t *testing.T) (*storage.Store, http.Handler) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, authenticatedRoutes(t, NewServer(db, nil, nil, nil))
}

func TestDeleteTerminalJobSucceeds(t *testing.T) {
	db, handler := newDeleteJobTestServer(t)
	ctx := context.Background()

	job, err := db.CreateJob(ctx, storage.JobKindUser, "someone", "用户 @someone")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	job.Status = storage.JobFailed
	job.Progress = 1
	if err := db.UpdateJob(ctx, job); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodDelete, "/api/jobs/"+strconv.FormatInt(job.ID, 10), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", response.Code, response.Body.String())
	}
	if _, err := db.GetJob(ctx, job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job still present after delete: %v", err)
	}
}

func TestDeleteActiveJobReturnsConflict(t *testing.T) {
	db, handler := newDeleteJobTestServer(t)
	ctx := context.Background()

	job, err := db.CreateJob(ctx, storage.JobKindUser, "someone", "")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodDelete, "/api/jobs/"+strconv.FormatInt(job.ID, 10), nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s, want 409", response.Code, response.Body.String())
	}
	if _, err := db.GetJob(ctx, job.ID); err != nil {
		t.Fatalf("active job must survive delete: %v", err)
	}
}

func TestDeleteMissingJobReturnsNotFound(t *testing.T) {
	_, handler := newDeleteJobTestServer(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodDelete, "/api/jobs/999999", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404", response.Code, response.Body.String())
	}
}
