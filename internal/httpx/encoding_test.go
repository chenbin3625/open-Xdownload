package httpx

import "testing"

func TestNegotiateEncoding(t *testing.T) {
	cases := []struct {
		header    string
		supported []string
		want      string
	}{
		{"gzip, deflate, br", []string{"br", "gzip"}, "br"},
		{"gzip, deflate, br", []string{"zstd", "gzip"}, "gzip"},
		{"gzip", []string{"br", "gzip"}, "gzip"},
		{"", []string{"br", "gzip"}, ""},
		{"br;q=0, gzip", []string{"br", "gzip"}, "gzip"},
		{"gzip;q=0, br;q=0", []string{"br", "gzip"}, ""},
		{"gzip;q=0.5, br;q=0.9", []string{"br", "gzip"}, "br"},
		{"GZip, BR", []string{"br", "gzip"}, "br"},
		{"deflate", []string{"br", "gzip"}, ""},
		{"*", []string{"br", "gzip"}, "br"},
		{"br;q=0, *", []string{"br", "gzip"}, "gzip"},
		{"*;q=0", []string{"br", "gzip"}, ""},
	}
	for _, tc := range cases {
		got := NegotiateEncoding(tc.header, tc.supported...)
		if got != tc.want {
			t.Errorf("NegotiateEncoding(%q, %v) = %q, want %q", tc.header, tc.supported, got, tc.want)
		}
	}
}

func TestNegotiateEncodingIgnoresMalformedParams(t *testing.T) {
	// q without a numeric value, or a value out of range, must not be treated
	// as an exclusion — behave as if no parameter was given.
	if got := NegotiateEncoding("br;q=, gzip", "br", "gzip"); got != "br" {
		t.Fatalf("malformed q should be ignored, got %q", got)
	}
	if got := NegotiateEncoding("gzip; q=2", "br", "gzip"); got != "gzip" {
		t.Fatalf("out-of-range q should be ignored, got %q", got)
	}
}
