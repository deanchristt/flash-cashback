package domain

import "errors"

// Sentinel errors surfaced to the transport layer, which maps each to an HTTP
// status. Keeping them here keeps business meaning out of the HTTP package.
var (
	// ErrInvalidAmount is returned for non-positive payment or redemption amounts.
	ErrInvalidAmount = errors.New("amount must be a positive integer (IDR)")

	// ErrUserRequired is returned when the caller's user identity is missing.
	ErrUserRequired = errors.New("user id is required")

	// ErrIdempotencyKeyRequired is returned when a write is missing its key.
	ErrIdempotencyKeyRequired = errors.New("idempotency key is required")

	// ErrInsufficientBalance is returned when a redemption exceeds the balance.
	ErrInsufficientBalance = errors.New("insufficient cashback balance")
)
