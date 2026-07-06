package main

import (
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/webui"
)

func withWebApp(api http.Handler, webDir string) http.Handler {
	if fsys, ok := externalWebApp(webDir); ok {
		return webAppHandler(api, fsys)
	}
	if fsys, ok := webui.FileSystem(); ok {
		return webAppHandler(api, fsys)
	}
	return api
}

func externalWebApp(webDir string) (http.FileSystem, bool) {
	if webDir == "" {
		return nil, false
	}
	info, err := os.Stat(webDir)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	fsys := http.Dir(webDir)
	if !fileExists(fsys, "index.html") {
		return nil, false
	}
	return fsys, true
}

func webAppHandler(api http.Handler, fsys http.FileSystem) http.Handler {
	fileServer := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if fileExists(fsys, name) {
			fileServer.ServeHTTP(w, r)
			return
		}

		serveIndex(w, fsys)
	})
}

func fileExists(fsys http.FileSystem, name string) bool {
	if name == "" {
		return false
	}
	file, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	return err == nil && !stat.IsDir()
}

func serveIndex(w http.ResponseWriter, fsys http.FileSystem) {
	file, err := fsys.Open("index.html")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, file)
}
