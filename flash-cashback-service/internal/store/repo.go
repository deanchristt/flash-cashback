package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flashcashback/internal/domain"
)

// Repo is the Postgres-backed money store. Every money-mutating operation runs
// in a single database transaction so it is atomic and crash-safe.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ---- Inputs / outputs -------------------------------------------------------

type EarnInput struct {
	UserID         string
	Amount         int64
	IdempotencyKey string
	Now            time.Time // injected for testability and correct WIB bucketing
}

type EarnResult struct {
	PaymentID       int64  `json:"payment_id"`
	Amount          int64  `json:"amount"`
	CashbackAwarded int64  `json:"cashback_awarded"`
	Status          string `json:"status"`
	Balance         int64  `json:"balance"`
	Replayed        bool   `json:"replayed"` // true if this was an idempotent retry
}

type RedeemInput struct {
	UserID         string
	Amount         int64
	IdempotencyKey string
}

type RedeemResult struct {
	RedemptionID int64 `json:"redemption_id"`
	Redeemed     int64 `json:"redeemed"`
	Balance      int64 `json:"balance"`
	Replayed     bool  `json:"replayed"`
}

type LedgerEntry struct {
	ID           int64     `json:"id"`
	EntryType    string    `json:"entry_type"`
	Amount       int64     `json:"amount"`
	BalanceAfter int64     `json:"balance_after"`
	SourceType   string    `json:"source_type"`
	SourceID     string    `json:"source_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type Campaign struct {
	TotalBudget int64 `json:"total_budget"`
	Remaining   int64 `json:"remaining"`
	Active      bool  `json:"active"`
}

// ---- Earn -------------------------------------------------------------------

// ProcessPayment records a payment and, if eligible, awards cashback — all
// atomically. Concurrency safety rests on three things done inside one tx:
//
//  1. Idempotency: INSERT ... ON CONFLICT DO NOTHING on (user_id,
//     idempotency_key). A retry finds the existing payment and returns its
//     original outcome instead of awarding again.
//  2. Daily cap: the user's daily row is locked FOR UPDATE before the tally is
//     read and updated, so concurrent payments for the same user serialize and
//     cannot jointly exceed 50,000/day.
//  3. Budget: the single campaign_budget row is locked FOR UPDATE, so
//     concurrent awards across all users serialize on it and cannot overspend
//     the 10,000,000 total.
//
// Lock order is always (user daily row) then (global budget row); since the
// budget row is a single shared row acquired last, no deadlock cycle is
// possible.
func (r *Repo) ProcessPayment(ctx context.Context, in EarnInput) (EarnResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return EarnResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// 1. Idempotent insert of the payment.
	var paymentID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (user_id, amount, idempotency_key, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING id`,
		in.UserID, in.Amount, in.IdempotencyKey,
	).Scan(&paymentID)

	if errors.Is(err, pgx.ErrNoRows) {
		// Replay: the payment already exists. Return its stored outcome.
		res, lerr := loadExistingPayment(ctx, tx, in.UserID, in.IdempotencyKey)
		if lerr != nil {
			return EarnResult{}, lerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return EarnResult{}, fmt.Errorf("commit replay: %w", cerr)
		}
		return res, nil
	}
	if err != nil {
		return EarnResult{}, fmt.Errorf("insert payment: %w", err)
	}

	// 2. Eligibility by amount alone.
	gross := domain.GrossCashback(in.Amount)
	if gross == 0 {
		bal, err := balanceTx(ctx, tx, in.UserID)
		if err != nil {
			return EarnResult{}, err
		}
		if err := finalizePayment(ctx, tx, paymentID, 0, domain.StatusNoCashback); err != nil {
			return EarnResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return EarnResult{}, fmt.Errorf("commit no-cashback: %w", err)
		}
		return EarnResult{PaymentID: paymentID, Amount: in.Amount, CashbackAwarded: 0,
			Status: domain.StatusNoCashback, Balance: bal}, nil
	}

	// 3. Lock the per-user daily tally, then the global budget.
	day := domain.JakartaDay(in.Now)
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_daily_cashback (user_id, day, earned)
		VALUES ($1, $2, 0)
		ON CONFLICT (user_id, day) DO NOTHING`,
		in.UserID, day,
	); err != nil {
		return EarnResult{}, fmt.Errorf("ensure daily row: %w", err)
	}
	var earnedToday int64
	if err := tx.QueryRow(ctx,
		`SELECT earned FROM user_daily_cashback WHERE user_id = $1 AND day = $2 FOR UPDATE`,
		in.UserID, day,
	).Scan(&earnedToday); err != nil {
		return EarnResult{}, fmt.Errorf("lock daily row: %w", err)
	}
	dailyRemaining := domain.DailyRemaining(earnedToday)

	var budgetRemaining int64
	if err := tx.QueryRow(ctx,
		`SELECT remaining FROM campaign_budget WHERE id = 1 FOR UPDATE`,
	).Scan(&budgetRemaining); err != nil {
		return EarnResult{}, fmt.Errorf("lock budget row: %w", err)
	}

	award := domain.AwardableCashback(gross, dailyRemaining, budgetRemaining)
	status := domain.EarnStatus(in.Amount, gross, award, dailyRemaining, budgetRemaining)

	// 3a. Nothing awardable (cap or budget exhausted): record and stop.
	if award == 0 {
		bal, err := balanceTx(ctx, tx, in.UserID)
		if err != nil {
			return EarnResult{}, err
		}
		if err := finalizePayment(ctx, tx, paymentID, 0, status); err != nil {
			return EarnResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return EarnResult{}, fmt.Errorf("commit zero-award: %w", err)
		}
		return EarnResult{PaymentID: paymentID, Amount: in.Amount, CashbackAwarded: 0,
			Status: status, Balance: bal}, nil
	}

	// 4. Apply the award atomically: daily tally, budget, balance, ledger.
	if _, err := tx.Exec(ctx,
		`UPDATE user_daily_cashback SET earned = earned + $1 WHERE user_id = $2 AND day = $3`,
		award, in.UserID, day,
	); err != nil {
		return EarnResult{}, fmt.Errorf("update daily tally: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE campaign_budget SET remaining = remaining - $1, updated_at = now() WHERE id = 1`,
		award,
	); err != nil {
		return EarnResult{}, fmt.Errorf("decrement budget: %w", err)
	}

	var newBalance int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO user_balance (user_id, balance)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		    SET balance = user_balance.balance + EXCLUDED.balance, updated_at = now()
		RETURNING balance`,
		in.UserID, award,
	).Scan(&newBalance); err != nil {
		return EarnResult{}, fmt.Errorf("update balance: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO cashback_ledger
		    (user_id, entry_type, amount, balance_after, source_type, source_id)
		VALUES ($1, 'EARN', $2, $3, 'payment', $4)`,
		in.UserID, award, newBalance, strconv.FormatInt(paymentID, 10),
	); err != nil {
		return EarnResult{}, fmt.Errorf("insert ledger: %w", err)
	}

	if err := finalizePayment(ctx, tx, paymentID, award, status); err != nil {
		return EarnResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EarnResult{}, fmt.Errorf("commit award: %w", err)
	}

	return EarnResult{PaymentID: paymentID, Amount: in.Amount, CashbackAwarded: award,
		Status: status, Balance: newBalance}, nil
}

func loadExistingPayment(ctx context.Context, tx pgx.Tx, userID, key string) (EarnResult, error) {
	var res EarnResult
	res.Replayed = true
	err := tx.QueryRow(ctx,
		`SELECT id, amount, cashback_awarded, status FROM payments
		 WHERE user_id = $1 AND idempotency_key = $2`,
		userID, key,
	).Scan(&res.PaymentID, &res.Amount, &res.CashbackAwarded, &res.Status)
	if err != nil {
		return EarnResult{}, fmt.Errorf("load existing payment: %w", err)
	}
	bal, err := balanceTx(ctx, tx, userID)
	if err != nil {
		return EarnResult{}, err
	}
	res.Balance = bal
	return res, nil
}

func finalizePayment(ctx context.Context, tx pgx.Tx, id, awarded int64, status string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE payments SET cashback_awarded = $1, status = $2 WHERE id = $3`,
		awarded, status, id,
	); err != nil {
		return fmt.Errorf("finalize payment: %w", err)
	}
	return nil
}

// ---- Redeem -----------------------------------------------------------------

// Redeem debits a user's cashback balance atomically and idempotently. The
// balance decrement is a single conditional UPDATE (... WHERE balance >= amount)
// which, combined with the row lock it takes, makes concurrent redemptions
// serialize and prevents the balance from ever going negative — the storage-level
// CHECK (balance >= 0) is the final backstop.
func (r *Repo) Redeem(ctx context.Context, in RedeemInput) (RedeemResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return RedeemResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Idempotent insert of the redemption intent.
	var redemptionID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO redemptions (user_id, amount, idempotency_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING id`,
		in.UserID, in.Amount, in.IdempotencyKey,
	).Scan(&redemptionID)

	if errors.Is(err, pgx.ErrNoRows) {
		// Replay of a previously successful redemption.
		var res RedeemResult
		res.Replayed = true
		if err := tx.QueryRow(ctx,
			`SELECT id, amount, balance_after FROM redemptions
			 WHERE user_id = $1 AND idempotency_key = $2`,
			in.UserID, in.IdempotencyKey,
		).Scan(&res.RedemptionID, &res.Redeemed, &res.Balance); err != nil {
			return RedeemResult{}, fmt.Errorf("load existing redemption: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RedeemResult{}, fmt.Errorf("commit redeem replay: %w", err)
		}
		return res, nil
	}
	if err != nil {
		return RedeemResult{}, fmt.Errorf("insert redemption: %w", err)
	}

	// Atomic, guarded debit.
	var newBalance int64
	err = tx.QueryRow(ctx,
		`UPDATE user_balance SET balance = balance - $1, updated_at = now()
		 WHERE user_id = $2 AND balance >= $1
		 RETURNING balance`,
		in.Amount, in.UserID,
	).Scan(&newBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either no balance row or insufficient funds. Roll back so the
		// idempotency key is not consumed and can be retried later.
		return RedeemResult{}, domain.ErrInsufficientBalance
	}
	if err != nil {
		return RedeemResult{}, fmt.Errorf("debit balance: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO cashback_ledger
		    (user_id, entry_type, amount, balance_after, source_type, source_id)
		VALUES ($1, 'REDEEM', $2, $3, 'redemption', $4)`,
		in.UserID, in.Amount, newBalance, strconv.FormatInt(redemptionID, 10),
	); err != nil {
		return RedeemResult{}, fmt.Errorf("insert ledger: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE redemptions SET balance_after = $1 WHERE id = $2`,
		newBalance, redemptionID,
	); err != nil {
		return RedeemResult{}, fmt.Errorf("update redemption: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RedeemResult{}, fmt.Errorf("commit redeem: %w", err)
	}

	return RedeemResult{RedemptionID: redemptionID, Redeemed: in.Amount, Balance: newBalance}, nil
}

// ---- Reads ------------------------------------------------------------------

func (r *Repo) Balance(ctx context.Context, userID string) (int64, error) {
	return balanceQuerier(ctx, r.pool, userID)
}

func (r *Repo) History(ctx context.Context, userID string, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, entry_type, amount, balance_after, source_type, source_id, created_at
		FROM cashback_ledger
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	entries := make([]LedgerEntry, 0, limit)
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.EntryType, &e.Amount, &e.BalanceAfter,
			&e.SourceType, &e.SourceID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (r *Repo) CampaignStatus(ctx context.Context) (Campaign, error) {
	var c Campaign
	if err := r.pool.QueryRow(ctx,
		`SELECT total_budget, remaining FROM campaign_budget WHERE id = 1`,
	).Scan(&c.TotalBudget, &c.Remaining); err != nil {
		return Campaign{}, fmt.Errorf("query campaign: %w", err)
	}
	c.Active = c.Remaining > 0
	return c, nil
}

// ---- helpers ----------------------------------------------------------------

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func balanceTx(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	return balanceQuerier(ctx, tx, userID)
}

func balanceQuerier(ctx context.Context, q querier, userID string) (int64, error) {
	var bal int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM user_balance WHERE user_id = $1`, userID,
	).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query balance: %w", err)
	}
	return bal, nil
}
