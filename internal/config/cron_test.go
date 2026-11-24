package config

import (
	"testing"
	"time"
)

func TestCronScheduleNext(t *testing.T) {
	base := time.Date(2026, 11, 24, 2, 15, 0, 0, time.UTC)
	next, err := NextCronRun("0 3 * * *", "UTC", base)
	if err != nil {
		t.Fatalf("NextCronRun: %v", err)
	}
	want := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}

	nextStep, err := NextCronRun("*/15 * * * *", "UTC", base)
	if err != nil {
		t.Fatalf("step cron: %v", err)
	}
	wantStep := time.Date(2026, 11, 24, 2, 30, 0, 0, time.UTC)
	if !nextStep.Equal(wantStep) {
		t.Fatalf("got %v, want %v", nextStep, wantStep)
	}

	nextMacro, err := NextCronRun("@daily", "Asia/Shanghai", time.Date(2026, 11, 24, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("macro cron: %v", err)
	}
	// Asia/Shanghai midnight = 16:00 UTC
	wantMacro := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	if !nextMacro.Equal(wantMacro) {
		t.Fatalf("got %v, want %v", nextMacro, wantMacro)
	}

	if err := ValidateCron("bad cron", "UTC"); err == nil {
		t.Fatal("expected invalid cron error")
	}
	if err := ValidateCron("0 3 * * *", "Invalid/Zone"); err == nil {
		t.Fatal("expected invalid timezone error")
	}
}
