// Package observability provides structured logging and lightweight in-process
// metrics. Because this service moves money, the metrics are chosen so an
// operator can answer "is money flowing correctly and how much budget is left?"
// at a glance. Metrics are exposed in Prometheus text format at /metrics with no
// external dependency.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// Logger builds a structured logger. Production uses JSON (machine-parseable for
// log aggregation); dev uses a readable text handler.
func Logger(env string) *slog.Logger {
	var h slog.Handler
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if env == "prod" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// Metrics holds process-lifetime counters. All access is atomic and lock-free.
type Metrics struct {
	PaymentsTotal        atomic.Int64
	CashbackAwardedTotal atomic.Int64 // in IDR
	NoCashbackTotal      atomic.Int64
	PartialAwardTotal    atomic.Int64
	BudgetExhaustedTotal atomic.Int64
	DailyCapReachedTotal atomic.Int64
	RedemptionsTotal     atomic.Int64
	RedeemedAmountTotal  atomic.Int64 // in IDR
	RateLimitedTotal     atomic.Int64
	// BudgetRemaining is refreshed from the DB on each /metrics scrape so it is
	// always accurate rather than an approximation kept in memory.
	budgetRemaining atomic.Int64
}

func NewMetrics() *Metrics { return &Metrics{} }

// RecordEarn updates counters for a completed earn attempt.
func (m *Metrics) RecordEarn(status string, awarded int64) {
	m.PaymentsTotal.Add(1)
	if awarded > 0 {
		m.CashbackAwardedTotal.Add(awarded)
	}
	switch status {
	case "no_cashback":
		m.NoCashbackTotal.Add(1)
	case "partial_daily_cap", "partial_budget":
		m.PartialAwardTotal.Add(1)
	case "budget_exhausted":
		m.BudgetExhaustedTotal.Add(1)
	case "daily_cap_reached":
		m.DailyCapReachedTotal.Add(1)
	}
}

func (m *Metrics) RecordRedeem(amount int64) {
	m.RedemptionsTotal.Add(1)
	m.RedeemedAmountTotal.Add(amount)
}

func (m *Metrics) RecordRateLimited() { m.RateLimitedTotal.Add(1) }

func (m *Metrics) SetBudgetRemaining(v int64) { m.budgetRemaining.Store(v) }

// WritePrometheus renders the metrics in Prometheus text exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	g := func(name, help, typ string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, v)
	}
	g("cashback_payments_total", "Total payments processed", "counter", m.PaymentsTotal.Load())
	g("cashback_awarded_idr_total", "Total cashback awarded (IDR)", "counter", m.CashbackAwardedTotal.Load())
	g("cashback_no_cashback_total", "Payments below the minimum", "counter", m.NoCashbackTotal.Load())
	g("cashback_partial_award_total", "Awards limited by cap or budget", "counter", m.PartialAwardTotal.Load())
	g("cashback_budget_exhausted_total", "Zero-awards due to budget", "counter", m.BudgetExhaustedTotal.Load())
	g("cashback_daily_cap_reached_total", "Zero-awards due to daily cap", "counter", m.DailyCapReachedTotal.Load())
	g("cashback_redemptions_total", "Total redemptions", "counter", m.RedemptionsTotal.Load())
	g("cashback_redeemed_idr_total", "Total cashback redeemed (IDR)", "counter", m.RedeemedAmountTotal.Load())
	g("cashback_rate_limited_total", "Requests rejected by rate limiter", "counter", m.RateLimitedTotal.Load())
	g("cashback_budget_remaining_idr", "Remaining campaign budget (IDR)", "gauge", m.budgetRemaining.Load())
}
