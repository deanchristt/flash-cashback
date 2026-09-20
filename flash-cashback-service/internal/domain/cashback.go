// Package domain holds the pure Flash Cashback business rules. It has no
// dependency on databases, HTTP, or Redis so the money-critical arithmetic can
// be unit-tested in isolation and reasoned about on its own.
//
// Money representation
//
// All monetary values are int64 IDR minor units. In practice the Indonesian
// Rupiah has no circulating sub-unit, so one unit == one rupiah. We never use
// floating point for money: 5% of a payment can produce a fraction, and float
// rounding drift is unacceptable when it accumulates over millions of
// transactions. Fractions are floored (see GrossCashback), which is the
// conservative choice for the campaign budget and is documented for the user.
package domain

// Campaign rules. These are compile-time defaults; the campaign budget itself
// lives in the database (campaign_budget table) so it can be a single, durable,
// concurrently-decremented source of truth rather than a process constant.
const (
	// CashbackRateNumerator / CashbackRateDenominator express the 5% rate as an
	// exact integer ratio to avoid float math (amount * 5 / 100).
	CashbackRateNumerator   int64 = 5
	CashbackRateDenominator int64 = 100

	// MinPaymentForCashback: payments strictly below this earn nothing.
	MinPaymentForCashback int64 = 20_000

	// DailyCapPerUser: maximum cashback a single user can earn per calendar day
	// (Asia/Jakarta). See clock.go for the day-boundary definition.
	DailyCapPerUser int64 = 50_000
)

// GrossCashback returns the raw cashback a payment would earn under the rate
// rule alone, before the daily cap and campaign budget are applied. Payments
// below MinPaymentForCashback earn nothing. The result is floored to whole
// rupiah.
//
//	GrossCashback(19_999) == 0        // below threshold
//	GrossCashback(20_000) == 1_000    // exactly 5%
//	GrossCashback(20_001) == 1_000    // 1000.05 floored
func GrossCashback(amount int64) int64 {
	if amount < MinPaymentForCashback {
		return 0
	}
	return amount * CashbackRateNumerator / CashbackRateDenominator // integer floor
}

// DailyRemaining returns how much cashback a user may still earn today given
// how much they have already earned. Never negative.
func DailyRemaining(earnedToday int64) int64 {
	rem := DailyCapPerUser - earnedToday
	if rem < 0 {
		return 0
	}
	return rem
}

// AwardableCashback caps the gross cashback by what still fits under both the
// per-user daily cap and the remaining campaign budget. We award the largest
// amount that fits (partial award) rather than all-or-nothing: this pays users
// what the rules allow and lets the budget land exactly on its total.
func AwardableCashback(gross, dailyRemaining, budgetRemaining int64) int64 {
	award := gross
	if dailyRemaining < award {
		award = dailyRemaining
	}
	if budgetRemaining < award {
		award = budgetRemaining
	}
	if award < 0 {
		return 0
	}
	return award
}

// Payment outcome statuses. These are recorded on the payment and returned to
// the client for UX and observability; they are not themselves money-critical.
const (
	StatusNoCashback      = "no_cashback"       // below the minimum payment
	StatusEarned          = "earned"            // full 5% awarded
	StatusPartialDailyCap = "partial_daily_cap" // limited by the daily cap
	StatusPartialBudget   = "partial_budget"    // limited by remaining budget
	StatusDailyCapReached = "daily_cap_reached" // 0 awarded: cap already hit
	StatusBudgetExhausted = "budget_exhausted"  // 0 awarded: budget is gone
)

// EarnStatus classifies the outcome of an earn attempt so the client and
// operators can tell *why* an award was full, partial, or zero.
func EarnStatus(amount, gross, award, dailyRemaining, budgetRemaining int64) string {
	if amount < MinPaymentForCashback {
		return StatusNoCashback
	}
	if award >= gross {
		return StatusEarned
	}
	// The binding constraint is whichever remaining allowance is smaller.
	budgetIsBinding := budgetRemaining <= dailyRemaining
	if award == 0 {
		if budgetIsBinding {
			return StatusBudgetExhausted
		}
		return StatusDailyCapReached
	}
	if budgetIsBinding {
		return StatusPartialBudget
	}
	return StatusPartialDailyCap
}
