package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONRejectsTrailingDataAndOversizedWhitespace(t *testing.T) {
	for _, body := range []string{`{}]`, `{} }`, `{} {}`, "{}" + strings.Repeat(" ", 2<<20)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var result map[string]any
		if err := decodeJSON(w, r, &result); err == nil {
			t.Errorf("accepted trailing request data of %d bytes", len(body))
		}
	}
	for _, body := range []string{`{}`, "{} \t\r\n"} {
		var result map[string]any
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if err := decodeJSON(httptest.NewRecorder(), r, &result); err != nil {
			t.Fatalf("rejected valid JSON: %v", err)
		}
	}
}
