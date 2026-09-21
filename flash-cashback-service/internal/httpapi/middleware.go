package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"flashcashback/internal/redisx"
)

type ctxKey string

const (
	ctxKeyRequestID ctxKey = "request_id"
	ctxKeyUserID    ctxKey = "user_id"

	// UserHeader carries the caller's identity. Authentication is out of scope
	// for this exercise ("assume you know who the user is"), so we trust this
	// header. In production it would be set by an auth gateway from a verified
	// token, never by the client directly.
	UserHeader = "X-User-Id"
)

// cors handles Cross-Origin Resource Sharing for browser clients (the Expo web
// build). Browsers send a preflight OPTIONS request before any call that carries
// a custom header such as X-User-Id; without this the preflight gets a 405 and
// the browser blocks the real request. Native mobile clients don't trigger CORS.
func cors(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if allowAll {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else if allowed[origin] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-Id, X-Request-Id")
				w.Header().Set("Access-Control-Max-Age", "300")
			}
			// Short-circuit the preflight request.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requestID assigns a request id (honoring an inbound one) for traceable logs.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverer converts panics into 500s so one bad request cannot crash the
// process mid-flight (an in-progress DB tx is rolled back by its deferred
// Rollback).
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"panic", rec,
						"path", r.URL.Path,
						"request_id", r.Context().Value(ctxKeyRequestID))
					writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// requestLogger logs method, path, status, and latency for each request.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", r.Context().Value(ctxKeyRequestID),
			)
		})
	}
}

// requireUser extracts and enforces the user identity header.
func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get(UserHeader)
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "user_required",
				"missing "+UserHeader+" header")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rateLimit throttles write requests per user. Fails open if Redis is down.
func rateLimit(rc *redisx.Client, perMinute int, onLimited func()) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, _ := r.Context().Value(ctxKeyUserID).(string)
			key := "ratelimit:" + userID
			if !rc.Allow(r.Context(), key, perMinute, time.Minute) {
				if onLimited != nil {
					onLimited()
				}
				writeError(w, http.StatusTooManyRequests, "rate_limited",
					"too many requests, slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func userIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUserID).(string)
	return v
}
