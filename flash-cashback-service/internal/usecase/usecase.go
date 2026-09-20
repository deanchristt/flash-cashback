// Package usecase is the application layer. It validates input, applies the
// campaign use cases via the store, and records metrics. It has no knowledge of
// HTTP. Time is injected so behavior is deterministic and testable.
package usecase

import (
	"context"
	"time"

	"flashcashback/internal/domain"
	"flashcashback/internal/observability"
	"flashcashback/internal/store"
)

// Now returns the current time; overridable in tests.
type Now func() time.Time

type Service struct {
	repo    *store.Repo
	metrics *observability.Metrics
	now     Now
}

func New(repo *store.Repo, metrics *observability.Metrics, now Now) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, metrics: metrics, now: now}
}

// MakePayment records a payment and awards eligible cashback.
func (s *Service) MakePayment(ctx context.Context, userID string, amount int64, idempotencyKey string) (store.EarnResult, error) {
	if userID == "" {
		return store.EarnResult{}, domain.ErrUserRequired
	}
	if amount <= 0 {
		return store.EarnResult{}, domain.ErrInvalidAmount
	}
	if idempotencyKey == "" {
		return store.EarnResult{}, domain.ErrIdempotencyKeyRequired
	}

	res, err := s.repo.ProcessPayment(ctx, store.EarnInput{
		UserID:         userID,
		Amount:         amount,
		IdempotencyKey: idempotencyKey,
		Now:            s.now(),
	})
	if err != nil {
		return store.EarnResult{}, err
	}
	// Only count metrics for first-time processing, not idempotent replays, so
	// counters reflect real economic activity.
	if !res.Replayed {
		s.metrics.RecordEarn(res.Status, res.CashbackAwarded)
	}
	return res, nil
}

// Redeem debits a user's cashback balance.
func (s *Service) Redeem(ctx context.Context, userID string, amount int64, idempotencyKey string) (store.RedeemResult, error) {
	if userID == "" {
		return store.RedeemResult{}, domain.ErrUserRequired
	}
	if amount <= 0 {
		return store.RedeemResult{}, domain.ErrInvalidAmount
	}
	if idempotencyKey == "" {
		return store.RedeemResult{}, domain.ErrIdempotencyKeyRequired
	}

	res, err := s.repo.Redeem(ctx, store.RedeemInput{
		UserID:         userID,
		Amount:         amount,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return store.RedeemResult{}, err
	}
	if !res.Replayed {
		s.metrics.RecordRedeem(res.Redeemed)
	}
	return res, nil
}

func (s *Service) Balance(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, domain.ErrUserRequired
	}
	return s.repo.Balance(ctx, userID)
}

func (s *Service) History(ctx context.Context, userID string, limit int) ([]store.LedgerEntry, error) {
	if userID == "" {
		return nil, domain.ErrUserRequired
	}
	return s.repo.History(ctx, userID, limit)
}

func (s *Service) Campaign(ctx context.Context) (store.Campaign, error) {
	c, err := s.repo.CampaignStatus(ctx)
	if err == nil {
		s.metrics.SetBudgetRemaining(c.Remaining)
	}
	return c, err
}
