package config

import (
	"path/filepath"
	"testing"
)

func TestStorageCredentialEnvNameRestriction(t *testing.T) {
	doc := DefaultStorage(filepath.Join(t.TempDir(), "downloads"))
	doc.Targets = append(doc.Targets, StorageTarget{
		ID:           "s3-main",
		Name:         "S3",
		Type:         "s3",
		Endpoint:     "https://s3.example.com",
		Bucket:       "media",
		AccessKeyEnv: "OPEN_XDOWNLOAD_AUTH_TOKEN",
	})
	if err := doc.Validate(); err == nil {
		t.Fatal("expected arbitrary env var name to be rejected")
	}
	doc.Targets[1].AccessKeyEnv = "OPEN_XDOWNLOAD_S3_ACCESS_KEY"
	doc.Targets[1].SecretKeyEnv = "OPEN_XDOWNLOAD_S3_SECRET_KEY"
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected OPEN_XDOWNLOAD_S3_* env var names to be accepted, got %v", err)
	}
}

func TestMergeStorageSecretsDoesNotReplayAcrossEndpointChange(t *testing.T) {
	oldDoc := StorageDocument{
		Enabled: true,
		Targets: []StorageTarget{{
			ID:        "s3-main",
			Name:      "S3",
			Type:      "s3",
			Endpoint:  "https://s3.trusted.example",
			Bucket:    "media",
			AccessKey: "real-access",
			SecretKey: "real-secret",
		}},
		Default: AssetRule{TargetID: "s3-main", DirectoryTemplate: "{username}", FilenameTemplate: "{tweet_id}.{ext}"},
		Rules:   map[AssetKind]AssetRule{},
	}

	// Same endpoint preserves secrets when placeholder is echoed.
	sameNext := oldDoc.Redacted()
	mergedSame, err := MergeStorageSecrets(sameNext, oldDoc)
	if err != nil {
		t.Fatalf("MergeStorageSecrets same endpoint: %v", err)
	}
	if mergedSame.Targets[0].AccessKey != "real-access" || mergedSame.Targets[0].SecretKey != "real-secret" {
		t.Fatalf("expected same-endpoint secrets to be preserved, got %+v", mergedSame.Targets[0])
	}

	// Changed endpoint must NOT replay old secrets.
	movedNext := oldDoc.Redacted()
	movedNext.Targets[0].Endpoint = "https://attacker.example"
	mergedMoved, err := MergeStorageSecrets(movedNext, oldDoc)
	if err != nil {
		t.Fatalf("MergeStorageSecrets moved endpoint: %v", err)
	}
	if mergedMoved.Targets[0].AccessKey != "" || mergedMoved.Targets[0].SecretKey != "" {
		t.Fatalf("expected moved-endpoint secrets to be cleared, got %+v", mergedMoved.Targets[0])
	}
}

func TestParseCookieInputLowercaseHeaderAndInjection(t *testing.T) {
	inputs, issues := ParseCookieInput("cookie: auth_token=tok123; ct0=csrf456")
	if len(issues) != 0 || len(inputs) != 1 {
		t.Fatalf("expected lowercase cookie header to parse, inputs=%+v issues=%+v", inputs, issues)
	}
	if inputs[0].AuthToken != "tok123" || inputs[0].CSRFToken != "csrf456" {
		t.Fatalf("unexpected parsed tokens: %+v", inputs[0])
	}

	inputs, issues = ParseCookieInput(`{"auth_token":"tok;injected","ct0":"csrf456"}`)
	if len(inputs) != 0 || len(issues) != 1 {
		t.Fatalf("expected cookie value with semicolon to be rejected, inputs=%+v issues=%+v", inputs, issues)
	}
}
