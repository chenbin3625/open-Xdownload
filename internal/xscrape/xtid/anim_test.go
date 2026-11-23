package xtid

import (
	"math"
	"testing"
)

func TestRoundTo2MatchesPython(t *testing.T) {
	// 期望值来自 CPython 3 的 round(x, 2)（按二进制精确值、五取偶）。
	cases := []struct {
		in   float64
		want float64
	}{
		{2.675, 2.67},
		{0.125, 0.12},
		{0.375, 0.38},
		{0.135, 0.14},
		{-0.125, -0.12},
		{-0.375, -0.38},
		{1.005, 1.0},
		{2.5, 2.5},
	}
	for _, tc := range cases {
		if got := roundTo2(tc.in); got != tc.want {
			t.Errorf("roundTo2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFloatToHex(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, ""},
		{1, "1"},
		{0.5, ".8"},
		{0.25, ".4"},
		{10.75, "A.C"},
	}
	for _, tc := range cases {
		if got := floatToHex(tc.in); got != tc.want {
			t.Errorf("floatToHex(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnimationKeyFromRowKnownVector(t *testing.T) {
	row := []float64{14, 163, 141, 87, 72, 167, 48, 5, 199, 178, 164}
	got, err := animationKeyFromRow(row, 80.0/4096)
	if err != nil {
		t.Fatal(err)
	}
	want := "1797900f851eb851eb850428f5c28f5c290428f5c28f5c290f851eb851eb8500"
	if got != want {
		t.Fatalf("animation key = %q, want %q", got, want)
	}
}

func TestSolve(t *testing.T) {
	if got := solve(48, 60, 360, true); got != 116 {
		t.Fatalf("solve(48,60,360,true) = %v, want 116", got)
	}
	if got := solve(5, 0, 1, false); math.Abs(got-0.02) > 1e-12 {
		t.Fatalf("solve(5,0,1,false) = %v, want 0.02", got)
	}
}

func TestCalcAnimationKeyGuardsEmptyPathsAndNonFinite(t *testing.T) {
	if _, err := calcAnimationKey(nil, []int{1}, []byte{0, 1, 2, 3, 4, 5}); err == nil {
		t.Fatal("expected error for empty animationPaths")
	}
	if _, err := animationKeyFromRow([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, math.NaN()); err == nil {
		t.Fatal("expected error for NaN targetTime")
	}
	if _, err := animationKeyFromRow([]float64{math.Inf(1), 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 0.1); err == nil {
		t.Fatal("expected error for Inf frame value")
	}
}

func TestParseAnimationPathsHandlesUnicodeCaseExpansion(t *testing.T) {
	page := []byte(`<svg id="loading-x-anim-0"><path d="M0 0"></path><path d="M0 0 10 10C1 2 3 4 5 6"></path><!-- İİİ --></svg>`)
	paths, err := parseAnimationPaths(page)
	if err != nil || len(paths) != 1 || paths[0] != "M0 0 10 10C1 2 3 4 5 6" {
		t.Fatalf("paths = %v, err = %v", paths, err)
	}
}
