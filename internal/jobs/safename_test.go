package jobs

import (
	"path/filepath"
	"testing"
)

func TestSafeNameNeutralizesDotSegments(t *testing.T) {
	cases := map[string]string{
		"..":        "unknown",
		".":         "unknown",
		"...":       "unknown",
		"  ..  ":    "unknown",
		"../../et":  "....et",
		"normal":    "normal",
		"a.b":       "a.b",
		"trailing.": "trailing",
		"CON":       "_CON",
		"nul.txt":   "_nul.txt",
	}
	for input, want := range cases {
		if got := safeName(input); got != want {
			t.Errorf("safeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSafeNameCannotEscapeRoot(t *testing.T) {
	root := "/downloads/users"
	for _, hostile := range []string{"..", " .. ", ".", "..."} {
		joined := filepath.Join(root, safeName(hostile))
		if joined == "/downloads" || joined == "/downloads/users/.." {
			t.Fatalf("safeName(%q) escaped: %s", hostile, joined)
		}
		if filepath.Dir(joined) != root {
			t.Fatalf("safeName(%q) left the parent dir: %s", hostile, joined)
		}
	}
}
