package domain

import (
	"testing"
	"time"
)

func TestGrossCashback(t *testing.T) {
	cases := []struct {
		name   string
		amount int64
		want   int64
	}{
		{"below threshold earns nothing", 19_999, 0},
		{"zero amount", 0, 0},
		{"exactly at threshold", 20_000, 1_000},
		{"just above threshold floors the fraction", 20_001, 1_000}, // 1000.05 -> 1000
		{"round amount", 100_000, 5_000},
		{"fraction floored", 99_999, 4_999}, // 4999.95 -> 4999
		{"large amount no overflow", 1_000_000_000, 50_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GrossCashback(tc.amount); got != tc.want {
				t.Fatalf("GrossCashback(%d) = %d, want %d", tc.amount, got, tc.want)
			}
		})
	}
}

func TestDailyRemaining(t *testing.T) {
	cases := []struct {
		earned int64
		want   int64
	}{
		{0, 50_000},
		{10_000, 40_000},
		{50_000, 0},
		{60_000, 0}, // never negative, even if over-earned somehow
	}
	for _, tc := range cases {
		if got := DailyRemaining(tc.earned); got != tc.want {
			t.Fatalf("DailyRemaining(%d) = %d, want %d", tc.earned, got, tc.want)
		}
	}
}

func TestAwardableCashback(t *testing.T) {
	cases := []struct {
		name                            string
		gross, dailyRem, budgetRem, want int64
	}{
		{"nothing binds", 1_000, 50_000, 10_000_000, 1_000},
		{"daily cap binds", 2_000, 1_000, 10_000_000, 1_000},
		{"budget binds", 2_000, 50_000, 500, 500},
		{"daily cap already exhausted", 1_000, 0, 10_000_000, 0},
		{"budget already exhausted", 1_000, 50_000, 0, 0},
		{"both zero", 1_000, 0, 0, 0},
		{"tightest of the two wins", 5_000, 3_000, 2_000, 2_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AwardableCashback(tc.gross, tc.dailyRem, tc.budgetRem)
			if got != tc.want {
				t.Fatalf("AwardableCashback(%d,%d,%d) = %d, want %d",
					tc.gross, tc.dailyRem, tc.budgetRem, got, tc.want)
			}
		})
	}
}

func TestEarnStatus(t *testing.T) {
	cases := []struct {
		name                                  string
		amount, gross, award, dailyRem, budgetRem int64
		want                                  string
	}{
		{"below minimum", 10_000, 0, 0, 50_000, 10_000_000, StatusNoCashback},
		{"full award", 20_000, 1_000, 1_000, 50_000, 10_000_000, StatusEarned},
		{"partial due to daily cap", 20_000, 1_000, 400, 400, 10_000_000, StatusPartialDailyCap},
		{"partial due to budget", 20_000, 1_000, 400, 50_000, 400, StatusPartialBudget},
		{"zero due to daily cap", 20_000, 1_000, 0, 0, 10_000_000, StatusDailyCapReached},
		{"zero due to budget", 20_000, 1_000, 0, 50_000, 0, StatusBudgetExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EarnStatus(tc.amount, tc.gross, tc.award, tc.dailyRem, tc.budgetRem)
			if got != tc.want {
				t.Fatalf("EarnStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJakartaDay(t *testing.T) {
	// 2026-09-20 23:30 UTC is already 2026-09-21 06:30 in Jakarta (UTC+7),
	// so it must bucket into the 21st, not the 20th. This is exactly the
	// boundary a naive UTC implementation would get wrong.
	utc := time.Date(2026, 9, 20, 23, 30, 0, 0, time.UTC)
	day := JakartaDay(utc)
	if y, m, d := day.Date(); y != 2026 || m != time.September || d != 21 {
		t.Fatalf("JakartaDay(%s) = %s, want 2026-09-21 (WIB)", utc, day)
	}

	// Just after Jakarta midnight stays on the same Jakarta day.
	local := time.Date(2026, 9, 21, 0, 15, 0, 0, Jakarta)
	if d := JakartaDay(local).Day(); d != 21 {
		t.Fatalf("JakartaDay local = day %d, want 21", d)
	}
}
