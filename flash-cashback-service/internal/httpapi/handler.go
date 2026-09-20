package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"flashcashback/internal/domain"
	"flashcashback/internal/observability"
	"flashcashback/internal/usecase"
)

type Handler struct {
	svc     *usecase.Service
	metrics *observability.Metrics
	logger  *slog.Logger
}

func NewHandler(svc *usecase.Service, metrics *observability.Metrics, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, metrics: metrics, logger: logger}
}

// ---- request bodies ---------------------------------------------------------

type paymentRequest struct {
	// Amount is the payment amount in whole IDR. Using json.Number rejects
	// fractional/float inputs that could smuggle rounding issues into money.
	Amount         json.Number `json:"amount"`
	IdempotencyKey string      `json:"idempotency_key"`
}

type redeemRequest struct {
	Amount         json.Number `json:"amount"`
	IdempotencyKey string      `json:"idempotency_key"`
}

// ---- handlers ---------------------------------------------------------------

func (h *Handler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	var req paymentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	amount, err := parseIDR(req.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_amount", err.Error())
		return
	}

	res, err := h.svc.MakePayment(r.Context(), userIDFrom(r.Context()), amount, req.IdempotencyKey)
	if err != nil {
		h.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (h *Handler) Redeem(w http.ResponseWriter, r *http.Request) {
	var req redeemRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	amount, err := parseIDR(req.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_amount", err.Error())
		return
	}

	res, err := h.svc.Redeem(r.Context(), userIDFrom(r.Context()), amount, req.IdempotencyKey)
	if err != nil {
		h.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	bal, err := h.svc.Balance(r.Context(), userIDFrom(r.Context()))
	if err != nil {
		h.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userIDFrom(r.Context()),
		"balance": bal,
	})
}

func (h *Handler) GetHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := h.svc.History(r.Context(), userIDFrom(r.Context()), limit)
	if err != nil {
		h.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (h *Handler) GetCampaign(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Campaign(r.Context())
	if err != nil {
		h.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	// Refresh the budget gauge from the DB so the scrape is accurate.
	if c, err := h.svc.Campaign(r.Context()); err == nil {
		h.metrics.SetBudgetRemaining(c.Remaining)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	h.metrics.WritePrometheus(w)
}

// ---- error mapping ----------------------------------------------------------

func (h *Handler) writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "invalid_amount", err.Error())
	case errors.Is(err, domain.ErrIdempotencyKeyRequired):
		writeError(w, http.StatusBadRequest, "idempotency_key_required", err.Error())
	case errors.Is(err, domain.ErrUserRequired):
		writeError(w, http.StatusUnauthorized, "user_required", err.Error())
	case errors.Is(err, domain.ErrInsufficientBalance):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_balance", err.Error())
	default:
		h.logger.Error("unhandled error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
