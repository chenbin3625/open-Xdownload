package config

import (
	"os"
	"path/filepath"
	"runtime"
)

type AppConfig struct {
	DownloadDir      string `json:"downloadDir" db:"download_dir"`
	MaxConcurrency   int    `json:"maxConcurrency" db:"max_concurrency"`
	ProxyURL         string `json:"proxyUrl" db:"proxy_url"`
	AuthToken        string `json:"authToken,omitempty" db:"auth_token"`
	CSRFToken        string `json:"csrfToken,omitempty" db:"csrf_token"`
	AutoRetryFailed  bool   `json:"autoRetryFailed" db:"auto_retry_failed"`
	KeepOriginalURLs bool   `json:"keepOriginalUrls" db:"keep_original_urls"`
}

func Default() AppConfig {
	return AppConfig{
		DownloadDir:      defaultDownloadDir(),
		MaxConcurrency:   min(8, max(2, runtime.GOMAXPROCS(0))),
		AutoRetryFailed:  true,
		KeepOriginalURLs: true,
	}
}

func (cfg AppConfig) Normalized() AppConfig {
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = defaultDownloadDir()
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = Default().MaxConcurrency
	}
	return cfg
}

func (cfg AppConfig) Redacted() AppConfig {
	cfg.AuthToken = redact(cfg.AuthToken)
	cfg.CSRFToken = redact(cfg.CSRFToken)
	return cfg
}

func defaultDownloadDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "downloads"
	}
	return filepath.Join(cwd, "downloads")
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
