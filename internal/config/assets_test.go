package config

import "testing"

func TestDefaultStorageUsesMediaTypeThenUser(t *testing.T) {
	document := DefaultStorage("/tmp/archive")
	if !document.Enabled {
		t.Fatal("default storage policy must be enabled")
	}
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range AssetKinds {
		rule, target, err := document.Rule(kind)
		if err != nil {
			t.Fatal(err)
		}
		if target.Root != "/tmp/archive" || rule.DirectoryTemplate != "{kind}/{username}" {
			t.Fatalf("kind %s: target = %+v, rule = %+v", kind, target, rule)
		}
	}
}

func TestStorageRuleInheritsEachDefaultField(t *testing.T) {
	document := DefaultStorage("/tmp/archive")
	document.Default.Prefix = "default-root"
	document.Rules = map[AssetKind]AssetRule{
		AssetVideo: {TargetID: "local", FilenameTemplate: "video_{tweet_id}.{ext}"},
	}

	rule, target, err := document.Rule(AssetVideo)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != "local" {
		t.Fatalf("target = %q, want local", target.ID)
	}
	if rule.Prefix != document.Default.Prefix {
		t.Fatalf("prefix = %q, want inherited %q", rule.Prefix, document.Default.Prefix)
	}
	if rule.DirectoryTemplate != document.Default.DirectoryTemplate {
		t.Fatalf("directory template = %q, want inherited %q", rule.DirectoryTemplate, document.Default.DirectoryTemplate)
	}
	if rule.FilenameTemplate != "video_{tweet_id}.{ext}" {
		t.Fatalf("filename template = %q, want category override", rule.FilenameTemplate)
	}
}

func TestStorageRuleExplicitRootDirectoryAndSeparateFolder(t *testing.T) {
	document := DefaultStorage("/tmp/archive")
	on, off := true, false
	document.Default.SeparateFolder = &on
	document.Rules[AssetImage] = AssetRule{DirectoryTemplateSet: true, SeparateFolder: &off}
	rule, _, err := document.Rule(AssetImage)
	if err != nil {
		t.Fatal(err)
	}
	if rule.DirectoryTemplate != "" || rule.SeparateFolder == nil || *rule.SeparateFolder {
		t.Fatalf("explicit root/disabled folder was lost: %+v", rule)
	}
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
}
