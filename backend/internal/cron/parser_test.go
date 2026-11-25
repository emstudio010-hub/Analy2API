package cron

import (
	"testing"
	"time"
)

func TestParseAndNext(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghai = time.FixedZone("CST", 8*3600)
	}

	base := time.Date(2026, 11, 15, 7, 45, 10, 0, shanghai) // 周日

	t.Run("daily_8_and_20", func(t *testing.T) {
		s, err := Parse("0 8,20 * * *")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		got := s.NextN(base, 3)
		want := []time.Time{
			time.Date(2026, 11, 15, 8, 0, 0, 0, shanghai),
			time.Date(2026, 11, 15, 20, 0, 0, 0, shanghai),
			time.Date(2026, 11, 16, 8, 0, 0, 0, shanghai),
		}
		if len(got) != len(want) {
			t.Fatalf("got %d items, want %d", len(got), len(want))
		}
		for i := range want {
			if !got[i].Equal(want[i]) {
				t.Fatalf("item %d = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("every_30_min", func(t *testing.T) {
		s, err := Parse("*/30 * * * *")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		got := s.NextN(base, 3)
		want := []time.Time{
			time.Date(2026, 11, 15, 8, 0, 0, 0, shanghai),
			time.Date(2026, 11, 15, 8, 30, 0, 0, shanghai),
			time.Date(2026, 11, 15, 9, 0, 0, 0, shanghai),
		}
		for i := range want {
			if !got[i].Equal(want[i]) {
				t.Fatalf("item %d = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("weekdays_only", func(t *testing.T) {
		s, err := Parse("0 9 * * 1-5")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		next := s.Next(base) // 2026-11-15 是周日，下个工作日 2026-11-16 周一
		want := time.Date(2026, 11, 16, 9, 0, 0, 0, shanghai)
		if !next.Equal(want) {
			t.Fatalf("got %v, want %v", next, want)
		}
	})

	t.Run("6_field_with_seconds", func(t *testing.T) {
		s, err := Parse("30 0 8 * * *")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		next := s.Next(base)
		want := time.Date(2026, 11, 15, 8, 0, 30, 0, shanghai)
		if !next.Equal(want) {
			t.Fatalf("got %v, want %v", next, want)
		}
	})

	t.Run("descriptors", func(t *testing.T) {
		daily, err := Parse("@daily")
		if err != nil {
			t.Fatalf("Parse @daily failed: %v", err)
		}
		wantDaily := time.Date(2026, 11, 16, 0, 0, 0, 0, shanghai)
		if got := daily.Next(base); !got.Equal(wantDaily) {
			t.Fatalf("@daily got %v, want %v", got, wantDaily)
		}

		every, err := Parse("@every 15m")
		if err != nil {
			t.Fatalf("Parse @every failed: %v", err)
		}
		wantEvery := base.Add(15 * time.Minute)
		if got := every.Next(base); !got.Equal(wantEvery) {
			t.Fatalf("@every 15m got %v, want %v", got, wantEvery)
		}
	})

	t.Run("dow_7_equals_sunday", func(t *testing.T) {
		s, err := Parse("0 10 * * 7")
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		next := s.Next(base)
		want := time.Date(2026, 11, 15, 10, 0, 0, 0, shanghai)
		if !next.Equal(want) {
			t.Fatalf("got %v, want %v", next, want)
		}
	})

	t.Run("invalid_expressions", func(t *testing.T) {
		invalid := []string{
			"",
			"* * *",
			"60 * * * *",
			"0 25 * * *",
			"0 0 32 * *",
			"0 0 * 13 *",
			"0 0 * * 8",
			"*/0 * * * *",
			"5-2 * * * *",
			"@unknown",
			"@every 0s",
			"@every bad",
		}
		for _, expr := range invalid {
			if _, err := Parse(expr); err == nil {
				t.Fatalf("expected error for %q, got nil", expr)
			}
		}
	})
}
