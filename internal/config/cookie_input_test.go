package config

import (
	"reflect"
	"testing"
)

func TestParseCookieInputHeaderLines(t *testing.T) {
	input := "a=b; auth_token=at-1; ct0=ct-1; guest_id=g1; lang=en\n" +
		"auth_token=at-2; ct0=ct-2\n" +
		"only_auth=auth-only"
	inputs, issues := ParseCookieInput(input)
	if len(inputs) != 2 {
		t.Fatalf("inputs = %+v, want 2", inputs)
	}
	if inputs[0].AuthToken != "at-1" || inputs[0].CSRFToken != "ct-1" || inputs[0].Index != 0 {
		t.Fatalf("inputs[0] = %+v", inputs[0])
	}
	wantExtra := map[string]string{"a": "b", "guest_id": "g1", "lang": "en"}
	if !reflect.DeepEqual(inputs[0].Extra, wantExtra) {
		t.Fatalf("extra = %+v, want %+v", inputs[0].Extra, wantExtra)
	}
	if inputs[1].AuthToken != "at-2" || inputs[1].CSRFToken != "ct-2" || len(inputs[1].Extra) != 0 || inputs[1].Index != 1 {
		t.Fatalf("inputs[1] = %+v", inputs[1])
	}
	if len(issues) != 1 || issues[0].Index != 2 || issues[0].Reason != "缺少 auth_token 和 ct0" {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestParseCookieInputBareValues(t *testing.T) {
	for _, input := range []string{"at-token ct0-token", "at-token,ct0-token"} {
		inputs, issues := ParseCookieInput(input)
		if len(issues) != 0 {
			t.Fatalf("%q issues = %+v", input, issues)
		}
		if len(inputs) != 1 || inputs[0].AuthToken != "at-token" || inputs[0].CSRFToken != "ct0-token" {
			t.Fatalf("%q inputs = %+v", input, inputs)
		}
	}
}

func TestParseCookieInputJSON(t *testing.T) {
	single := `{"auth_token":"at-1","ct0":"ct-1","lang":"en"}`
	inputs, issues := ParseCookieInput(single)
	if len(issues) != 0 || len(inputs) != 1 {
		t.Fatalf("single inputs=%+v issues=%+v", inputs, issues)
	}
	if inputs[0].AuthToken != "at-1" || inputs[0].Extra["lang"] != "en" {
		t.Fatalf("single input = %+v", inputs[0])
	}

	array := `[{"authToken":"at-1","csrfToken":"ct-1","foo":"bar"},` +
		`{"auth_token":"at-2"},` +
		`{"ct0":"ct-3"}]`
	inputs, issues = ParseCookieInput(array)
	if len(inputs) != 1 || inputs[0].AuthToken != "at-1" || inputs[0].Extra["foo"] != "bar" {
		t.Fatalf("array inputs = %+v", inputs)
	}
	if len(issues) != 2 || issues[0].Index != 1 || issues[1].Index != 2 {
		t.Fatalf("array issues = %+v, want indexes 1,2", issues)
	}
	if inputs[0].Index != 0 {
		t.Fatalf("array input index = %d, want 0", inputs[0].Index)
	}

	// 有效条目在中间时，Index 指向它在数组里的位置。
	inputs, issues = ParseCookieInput(`[{"auth_token":"at-1"},{"auth_token":"at-2","ct0":"ct-2"},"oops"]`)
	if len(inputs) != 1 || inputs[0].AuthToken != "at-2" || inputs[0].Index != 1 {
		t.Fatalf("middle input = %+v", inputs)
	}
	if len(issues) != 2 || issues[0].Index != 0 || issues[1].Index != 2 {
		t.Fatalf("middle issues = %+v, want indexes 0,2", issues)
	}
}

// 空行不计入条目序号；成功条目与 issue 共用同一套序号，调用方据此对应回输入。
func TestParseCookieInputIndexesSkipBlankLines(t *testing.T) {
	input := "auth_token=at-1; ct0=ct-1\n\n   \nonly-one-value\nauth_token=at-3; ct0=ct-3\n"
	inputs, issues := ParseCookieInput(input)
	if len(inputs) != 2 || inputs[0].Index != 0 || inputs[1].Index != 2 || inputs[1].AuthToken != "at-3" {
		t.Fatalf("inputs = %+v, want indexes 0 and 2", inputs)
	}
	if len(issues) != 1 || issues[0].Index != 1 {
		t.Fatalf("issues = %+v, want index 1", issues)
	}
}

// 与前端导入框一致：首行是完整 Cookie 请求头（含 guest_id），次行只有 auth_token。
func TestParseCookieInputBrowserHeaderWithIncompleteSecondLine(t *testing.T) {
	inputs, issues := ParseCookieInput("guest_id=v1%3A1; auth_token=at-1; ct0=ct-1\nauth_token=at-2")
	if len(inputs) != 1 || inputs[0].Index != 0 || inputs[0].AuthToken != "at-1" || inputs[0].CSRFToken != "ct-1" {
		t.Fatalf("inputs = %+v", inputs)
	}
	if inputs[0].Extra["guest_id"] != "v1%3A1" {
		t.Fatalf("extra = %+v, want guest_id kept", inputs[0].Extra)
	}
	if len(issues) != 1 || issues[0].Index != 1 || issues[0].Reason != "缺少 ct0" {
		t.Fatalf("issues = %+v, want index 1 missing ct0", issues)
	}
}

func TestParseCookieInputExtensionExport(t *testing.T) {
	input := `[{"name":"auth_token","value":"at-1"},` +
		`{"name":"ct0","value":"ct-1"},` +
		`{"name":"guest_id","value":"g1"},` +
		`{"name":"bad name","value":"x"}]`
	inputs, issues := ParseCookieInput(input)
	if len(issues) != 0 || len(inputs) != 1 {
		t.Fatalf("inputs=%+v issues=%+v", inputs, issues)
	}
	if inputs[0].AuthToken != "at-1" || inputs[0].CSRFToken != "ct-1" || inputs[0].Index != 0 {
		t.Fatalf("input = %+v", inputs[0])
	}
	if inputs[0].Extra["guest_id"] != "g1" {
		t.Fatalf("extra = %+v", inputs[0].Extra)
	}
	if _, exists := inputs[0].Extra["bad name"]; exists {
		t.Fatalf("invalid cookie name kept: %+v", inputs[0].Extra)
	}
}

func TestParseCookieInputExtraFiltering(t *testing.T) {
	// JSON 里保留不合法名字/含分号或换行的值必须被丢弃。
	input := `{"auth_token":"at","ct0":"ct",` +
		`"ok":"v","with-dash":"1","with.dot":"2","with_under":"3",` +
		`"bad name":"v","bad;value":"drop","semi":"a;b","newline":"a\nb"}`
	inputs, issues := ParseCookieInput(input)
	if len(issues) != 0 || len(inputs) != 1 {
		t.Fatalf("inputs=%+v issues=%+v", inputs, issues)
	}
	extra := inputs[0].Extra
	want := map[string]string{"ok": "v", "with-dash": "1", "with.dot": "2", "with_under": "3"}
	if !reflect.DeepEqual(extra, want) {
		t.Fatalf("extra = %+v, want %+v", extra, want)
	}
}

func TestParseCookieInputMissingFields(t *testing.T) {
	inputs, issues := ParseCookieInput("auth_token=at\nct0=ct")
	if len(inputs) != 0 {
		t.Fatalf("inputs = %+v, want none", inputs)
	}
	if len(issues) != 2 || issues[0].Index != 0 || issues[1].Index != 1 {
		t.Fatalf("issues = %+v", issues)
	}
	if issues[0].Reason != "缺少 ct0" || issues[1].Reason != "缺少 auth_token" {
		t.Fatalf("reasons = %q, %q", issues[0].Reason, issues[1].Reason)
	}

	inputs, issues = ParseCookieInput("foo=bar")
	if len(inputs) != 0 || len(issues) != 1 || issues[0].Reason != "缺少 auth_token 和 ct0" {
		t.Fatalf("missing both: inputs=%+v issues=%+v", inputs, issues)
	}
}

func TestParseCookieInputEmpty(t *testing.T) {
	if inputs, issues := ParseCookieInput("   \n "); len(inputs) != 0 || len(issues) != 0 {
		t.Fatalf("empty input = %+v %+v", inputs, issues)
	}
	if inputs, issues := ParseCookieInput(""); len(inputs) != 0 || len(issues) != 0 {
		t.Fatalf("blank input = %+v %+v", inputs, issues)
	}
}
