package jobs

import (
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// fakeXSource 只用于区分缓存身份，不实现任何方法（嵌入 nil 接口即可满足 xapi.Source）。
type fakeXSource struct{ xapi.Source }

func TestInvalidateXSourceRebuildsCachedSource(t *testing.T) {
	manager := &Manager{}
	calls := 0
	factory := func(config.AppConfig) (xapi.Source, error) {
		calls++
		return &fakeXSource{}, nil
	}
	manager.SetXSourceFactory(factory)
	cfg := config.AppConfig{AuthToken: "configured"}

	first, err := manager.XSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.XSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first != second {
		t.Fatalf("unequal config should reuse cached source: calls=%d reused=%t", calls, first == second)
	}

	manager.InvalidateXSource()
	third, err := manager.XSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || third == second {
		t.Fatalf("InvalidateXSource should rebuild source: calls=%d reused=%t", calls, third == second)
	}

	// 替换工厂同样要丢弃当前缓存。
	manager.SetXSourceFactory(factory)
	fourth, err := manager.XSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || fourth == third {
		t.Fatalf("SetXSourceFactory should invalidate cached source: calls=%d reused=%t", calls, fourth == third)
	}
}
