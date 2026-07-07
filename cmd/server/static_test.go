package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestWebAppHandlerReturns404ForMissingStaticAssets(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), http.FS(fstest.MapFS{
		"index.html":    {Data: []byte("<html></html>")},
		"assets/app.js": {Data: []byte("console.log('ok')")},
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("SPA route status = %d, want 200", response.Code)
	}
}
