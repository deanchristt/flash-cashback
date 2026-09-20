package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"flashcashback/internal/domain"
)

// These are integration tests: they require a real Postgres because the entire
// point is to prove the concurrency guarantees that only the database can
// enforce (row locks, atomic conditional updates, CHECK constraints). They are
// skipped unless TEST_DATABASE_URL is set.
//
//	createdb cashback_test
//	TEST_DATABASE_URL='postgres://localhost:5432/cashback_test?sslmode=disable' \
//	  go test ./internal/store/ -run Concurrency -v
//
// The docker-compose file also exposes such a database; see the README.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// reset truncates all data and seeds the campaign budget to the given total.
func reset(t *testing.T, pool *pgxpool.Pool, budget int64) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		TRUNCATE payments, redemptions, cashback_ledger,
		         user_balance, user_daily_cashback RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE campaign_budget SET total_budget = $1, remaining = $1 WHERE id = 1`, budget,
	); err != nil {
		t.Fatalf("seed budget: %v", err)
	}
}

// TestConcurrency_DailyCapNeverExceeded fires many concurrent payments for a
// single user, each of which would earn cashback, and asserts the user never
// earns more than the daily cap.
func TestConcurrency_DailyCapNeverExceeded(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	reset(t, pool, 10_000_000)
	repo := NewRepo(pool)
	ctx := context.Background()
	now := time.Now()

	const workers = 50
	// Each payment of 200,000 IDR earns 10,000 gross. 50 of them would be
	// 500,000, but the daily cap is 50,000 → exactly 5 payments' worth.
	const payment = 200_000

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.ProcessPayment(ctx, EarnInput{
				UserID:         "user-cap",
				Amount:         payment,
				IdempotencyKey: uuid.NewString(),
				Now:            now,
			})
			if err != nil {
				t.Errorf("payment error: %v", err)
			}
		}()
	}
	wg.Wait()

	bal, err := repo.Balance(ctx, "user-cap")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal != domain.DailyCapPerUser {
		t.Fatalf("balance = %d, want exactly the daily cap %d", bal, domain.DailyCapPerUser)
	}

	// The daily tally must also equal the cap, never above.
	var earned int64
	day := domain.JakartaDay(now)
	if err := pool.QueryRow(ctx,
		`SELECT earned FROM user_daily_cashback WHERE user_id = 'user-cap' AND day = $1`, day,
	).Scan(&earned); err != nil {
		t.Fatalf("read tally: %v", err)
	}
	if earned != domain.DailyCapPerUser {
		t.Fatalf("earned = %d, want %d", earned, domain.DailyCapPerUser)
	}

	assertLedgerMatchesBalance(t, pool, "user-cap")
}

// TestConcurrency_BudgetNeverOverspent drains a small budget with many
// concurrent payments from distinct users and asserts total awarded equals the
// budget exactly, with remaining landing on zero and never going negative.
func TestConcurrency_BudgetNeverOverspent(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	const budget = 100_000
	reset(t, pool, budget)
	repo := NewRepo(pool)
	ctx := context.Background()
	now := time.Now()

	const workers = 200
	// Each distinct user pays 20,000 → earns 1,000. 200 users would earn
	// 200,000 total, but the budget is 100,000 → exactly 100 awards fit.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := repo.ProcessPayment(ctx, EarnInput{
				UserID:         "budget-user-" + uuid.NewString(),
				Amount:         20_000,
				IdempotencyKey: uuid.NewString(),
				Now:            now,
			})
			if err != nil {
				t.Errorf("payment error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	var remaining, total int64
	if err := pool.QueryRow(ctx,
		`SELECT remaining, total_budget FROM campaign_budget WHERE id = 1`,
	).Scan(&remaining, &total); err != nil {
		t.Fatalf("read budget: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0 (budget should be fully consumed)", remaining)
	}

	var awarded int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM cashback_ledger WHERE entry_type = 'EARN'`,
	).Scan(&awarded); err != nil {
		t.Fatalf("sum awarded: %v", err)
	}
	if awarded != budget {
		t.Fatalf("total awarded = %d, want exactly the budget %d", awarded, budget)
	}
}

// TestConcurrency_NoDoubleRedeem hammers a funded balance with concurrent
// redemptions and asserts the balance never goes negative and total redeemed
// never exceeds the starting balance.
func TestConcurrency_NoDoubleRedeem(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	reset(t, pool, 10_000_000)
	repo := NewRepo(pool)
	ctx := context.Background()

	// Fund the user directly to 50,000.
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_balance (user_id, balance) VALUES ('redeemer', 50000)`,
	); err != nil {
		t.Fatalf("fund: %v", err)
	}

	const workers = 100
	// 100 concurrent attempts of 1,000 each would be 100,000, but only 50,000
	// is available → exactly 50 must succeed, 50 must fail cleanly.
	var success, insufficient int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Redeem(ctx, RedeemInput{
				UserID:         "redeemer",
				Amount:         1_000,
				IdempotencyKey: uuid.NewString(),
			})
			mu.Lock()
			defer mu.Unlock()
			switch err {
			case nil:
				success++
			case domain.ErrInsufficientBalance:
				insufficient++
			default:
				t.Errorf("unexpected redeem error: %v", err)
			}
		}()
	}
	wg.Wait()

	if success != 50 {
		t.Fatalf("successful redemptions = %d, want 50", success)
	}
	if insufficient != 50 {
		t.Fatalf("insufficient rejections = %d, want 50", insufficient)
	}
	bal, _ := repo.Balance(ctx, "redeemer")
	if bal != 0 {
		t.Fatalf("final balance = %d, want 0 (never negative)", bal)
	}
}

// TestConcurrency_IdempotentPayment sends the same payment (same idempotency
// key) concurrently many times and asserts cashback is awarded exactly once.
func TestConcurrency_IdempotentPayment(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	reset(t, pool, 10_000_000)
	repo := NewRepo(pool)
	ctx := context.Background()
	now := time.Now()

	key := uuid.NewString()
	const workers = 30
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.ProcessPayment(ctx, EarnInput{
				UserID:         "idem-user",
				Amount:         100_000, // would earn 5,000 if applied
				IdempotencyKey: key,
				Now:            now,
			})
			if err != nil {
				t.Errorf("payment error: %v", err)
			}
		}()
	}
	wg.Wait()

	bal, _ := repo.Balance(ctx, "idem-user")
	if bal != 5_000 {
		t.Fatalf("balance = %d, want 5000 (awarded exactly once)", bal)
	}
	var payments, ledger int64
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM payments WHERE user_id = 'idem-user'`).Scan(&payments)
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM cashback_ledger WHERE user_id = 'idem-user'`).Scan(&ledger)
	if payments != 1 {
		t.Fatalf("payment rows = %d, want 1", payments)
	}
	if ledger != 1 {
		t.Fatalf("ledger rows = %d, want 1", ledger)
	}
}

// assertLedgerMatchesBalance checks the invariant that a user's materialized
// balance equals the sum of their ledger (EARN positive, REDEEM negative).
func assertLedgerMatchesBalance(t *testing.T, pool *pgxpool.Pool, userID string) {
	t.Helper()
	ctx := context.Background()
	var ledgerSum, balance int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN entry_type = 'EARN' THEN amount ELSE -amount END), 0)
		FROM cashback_ledger WHERE user_id = $1`, userID,
	).Scan(&ledgerSum); err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM user_balance WHERE user_id = $1`, userID,
	).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if ledgerSum != balance {
		t.Fatalf("ledger sum %d != materialized balance %d", ledgerSum, balance)
	}
}
