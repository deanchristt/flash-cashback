package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"flashcashback/internal/observability"
	"flashcashback/internal/redisx"
	"flashcashback/internal/usecase"
)

// Deps bundles what the router needs.
type Deps struct {
	Service            *usecase.Service
	Metrics            *observability.Metrics
	Redis              *redisx.Client
	Logger             *slog.Logger
	RateLimitPerMinute int
	// Ready reports readiness (e.g. DB reachable) for /readyz.
	Ready func(ctx context.Context) error
}

// NewRouter wires middleware and routes.
func NewRouter(d Deps) http.Handler {
	h := NewHandler(d.Service, d.Metrics, d.Logger)

	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(recoverer(d.Logger))
	r.Use(requestLogger(d.Logger))

	// Liveness: process is up.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Readiness: dependencies reachable.
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if d.Ready != nil {
			if err := d.Ready(req.Context()); err != nil {
				writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	r.Get("/metrics", h.Metrics)

	r.Route("/v1", func(r chi.Router) {
		// Public campaign status.
		r.Get("/campaign", h.GetCampaign)

		// Authenticated (user-scoped) routes.
		r.Group(func(r chi.Router) {
			r.Use(requireUser)

			// Reads.
			r.Get("/cashback/balance", h.GetBalance)
			r.Get("/cashback/history", h.GetHistory)

			// Writes: rate-limited per user.
			r.Group(func(r chi.Router) {
				r.Use(rateLimit(d.Redis, d.RateLimitPerMinute, d.Metrics.RecordRateLimited))
				r.Post("/payments", h.CreatePayment)
				r.Post("/cashback/redeem", h.Redeem)
			})
		})
	})

	return r
}
