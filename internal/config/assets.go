package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type AssetKind string

const (
	AssetImage      AssetKind = "image"
	AssetVideo      AssetKind = "video"
	AssetThumbnail  AssetKind = "thumbnail"
	AssetVideoCover AssetKind = "video_cover"
)

var AssetKinds = []AssetKind{AssetImage, AssetVideo, AssetThumbnail, AssetVideoCover}

type StorageTarget struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Version      int64  `json:"version"`
	Root         string `json:"root,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Region       string `json:"region,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	AccessKeyEnv string `json:"accessKeyEnv,omitempty"`
	SecretKeyEnv string `json:"secretKeyEnv,omitempty"`
}

type AssetRule struct {
	TargetID             string `json:"targetId"`
	Prefix               string `json:"prefix"`
	DirectoryTemplate    string `json:"directoryTemplate"`
	FilenameTemplate     string `json:"filenameTemplate"`
	DirectoryTemplateSet bool   `json:"directoryTemplateSet,omitempty"`
	SeparateFolder       *bool  `json:"separateFolder,omitempty"`
}

type ThumbnailConfig struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Format  string `json:"format"`
	Quality int    `json:"quality"`
}

type StorageDocument struct {
	Version   int64                   `json:"version"`
	Enabled   bool                    `json:"enabled"`
	Targets   []StorageTarget         `json:"targets"`
	Default   AssetRule               `json:"default"`
	Rules     map[AssetKind]AssetRule `json:"rules"`
	Thumbnail ThumbnailConfig         `json:"thumbnail"`
}

func DefaultStorage(root string) StorageDocument {
	return StorageDocument{Targets: []StorageTarget{{ID: "local", Name: "Local", Type: "local", Root: root}}, Default: AssetRule{TargetID: "local", DirectoryTemplate: "{username}/{year}/{month}", FilenameTemplate: "{tweet_id}_{media_id}_{index}.{ext}"}, Rules: map[AssetKind]AssetRule{}, Thumbnail: ThumbnailConfig{Width: 640, Height: 640, Format: "jpeg", Quality: 82}}
}

func (d StorageDocument) Rule(kind AssetKind) (AssetRule, StorageTarget, error) {
	r := d.Default
	if override, ok := d.Rules[kind]; ok {
		// Each field is independently overridable. An omitted field keeps the
		// default rule so a category can change only its target or naming part.
		if override.TargetID != "" {
			r.TargetID = override.TargetID
		}
		if override.Prefix != "" {
			r.Prefix = override.Prefix
		}
		if override.DirectoryTemplateSet || override.DirectoryTemplate != "" {
			r.DirectoryTemplate = override.DirectoryTemplate
		}
		if override.SeparateFolder != nil {
			r.SeparateFolder = override.SeparateFolder
		}
		if override.FilenameTemplate != "" {
			r.FilenameTemplate = override.FilenameTemplate
		}
	}
	for _, target := range d.Targets {
		if target.ID == r.TargetID {
			return r, target, nil
		}
	}
	return r, StorageTarget{}, fmt.Errorf("存储目标不存在: %s", r.TargetID)
}

var targetIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var templateTokenPattern = regexp.MustCompile(`\{([^{}]+)\}`)
var templateTokens = map[string]bool{"author_id": true, "username": true, "tweet_id": true, "media_id": true, "index": true, "published_at": true, "year": true, "month": true, "day": true, "text": true, "ext": true, "kind": true}

func ValidateAssetTemplate(value string, filename bool) error {
	if len(value) > 600 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return fmt.Errorf("模板路径无效")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("模板不能越出存储目录")
		}
	}
	if filename && (strings.Contains(value, "/") || !strings.Contains(value, "{ext}")) {
		return fmt.Errorf("文件名模板必须包含 {ext}，且不能包含目录")
	}
	for _, token := range templateTokenPattern.FindAllStringSubmatch(value, -1) {
		if !templateTokens[token[1]] {
			return fmt.Errorf("未知模板变量: %s", token[1])
		}
	}
	rest := templateTokenPattern.ReplaceAllString(value, "")
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("模板括号无效")
	}
	return nil
}

func (d StorageDocument) Validate() error {
	if len(d.Targets) == 0 || len(d.Targets) > 32 {
		return fmt.Errorf("存储目标数量应为 1-32")
	}
	seen := map[string]bool{}
	for _, t := range d.Targets {
		if !targetIDPattern.MatchString(t.ID) || seen[t.ID] {
			return fmt.Errorf("存储目标 ID 无效或重复")
		}
		seen[t.ID] = true
		switch t.Type {
		case "local", "nas":
			if !filepath.IsAbs(t.Root) {
				return fmt.Errorf("本地或 NAS 目标必须使用绝对目录")
			}
			if err := ValidateDownloadRoot(t.Root); err != nil {
				return err
			}
		case "s3":
			u, err := url.Parse(t.Endpoint)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return fmt.Errorf("S3 endpoint 无效")
			}
			if t.Bucket == "" || strings.ContainsAny(t.Bucket, "/\\") {
				return fmt.Errorf("S3 bucket 无效")
			}
		default:
			return fmt.Errorf("不支持的存储类型: %s", t.Type)
		}
		for _, key := range []string{t.AccessKeyEnv, t.SecretKeyEnv} {
			if key != "" && !regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`).MatchString(key) {
				return fmt.Errorf("凭据环境变量名称无效")
			}
		}
	}
	for kind := range d.Rules {
		valid := false
		for _, k := range AssetKinds {
			valid = valid || kind == k
		}
		if !valid {
			return fmt.Errorf("无效的资产分类: %s", kind)
		}
	}
	for _, kind := range AssetKinds {
		r, _, err := d.Rule(kind)
		if err != nil {
			return err
		}
		if err := ValidateAssetTemplate(r.DirectoryTemplate, false); err != nil {
			return err
		}
		if err := ValidateAssetTemplate(r.FilenameTemplate, true); err != nil {
			return err
		}
		if err := ValidateAssetTemplate(r.Prefix, false); err != nil {
			return err
		}
		if strings.ContainsAny(r.Prefix, "{}") {
			return fmt.Errorf("根前缀不支持模板变量")
		}
	}
	if d.Thumbnail.Width < 16 || d.Thumbnail.Width > 4096 || d.Thumbnail.Height < 16 || d.Thumbnail.Height > 4096 || d.Thumbnail.Quality < 1 || d.Thumbnail.Quality > 100 {
		return fmt.Errorf("缩略图尺寸或质量超出范围")
	}
	if d.Thumbnail.Format != "jpeg" && d.Thumbnail.Format != "png" && d.Thumbnail.Format != "webp" {
		return fmt.Errorf("不支持的缩略图格式")
	}
	return nil
}

func (d StorageDocument) Redacted() StorageDocument {
	d.Targets = append([]StorageTarget(nil), d.Targets...)
	for i := range d.Targets {
		t := &d.Targets[i]
		for _, v := range []*string{&t.AccessKey, &t.SecretKey, &t.SessionToken} {
			if *v != "" {
				*v = SecretPlaceholder
			}
		}
	}
	return d
}

func MergeStorageSecrets(next, old StorageDocument) (StorageDocument, error) {
	for i := range next.Targets {
		t := &next.Targets[i]
		var prior StorageTarget
		for _, p := range old.Targets {
			if p.ID == t.ID {
				prior = p
				break
			}
		}
		for _, pair := range []struct {
			value *string
			old   string
		}{{&t.AccessKey, prior.AccessKey}, {&t.SecretKey, prior.SecretKey}, {&t.SessionToken, prior.SessionToken}} {
			switch *pair.value {
			case "", SecretPlaceholder:
				*pair.value = pair.old
			case ClearSentinel:
				*pair.value = ""
			}
		}
	}
	return next, next.Validate()
}
