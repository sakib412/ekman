package ekman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// Middleware wraps an http.Handler.
type Middleware = func(http.Handler) http.Handler

const (
	slogError = slog.LevelError
	slogInfo  = slog.LevelInfo
)

// reqState is per-request bookkeeping shared by ekman's middleware: inner layers fill in the
// user and route so the outer access log can report them.
type reqState struct {
	id     string
	logger *slog.Logger
	userID string
	route  string
}

type stateKey struct{}

func stateFrom(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	return st
}

func (st *reqState) log(ctx context.Context, level slog.Level, msg string, args ...any) {
	st.logger.Log(ctx, level, msg, append([]any{"request_id", st.id}, args...)...)
}

// RequestID returns the request id ekman assigned (or accepted from X-Request-Id).
func RequestID(ctx context.Context) string {
	if st := stateFrom(ctx); st != nil {
		return st.id
	}
	return ""
}

func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func requestIDMiddleware(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if !validRequestID(id) {
				id = newID()
			}
			w.Header().Set("X-Request-Id", id)
			st := &reqState{id: id, logger: logger}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stateKey{}, st)))
		})
	}
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler {
				panic(p)
			}
			if st := stateFrom(r.Context()); st != nil {
				st.log(r.Context(), slog.LevelError, "panic", "panic", p, "stack", string(debug.Stack()))
			}
			if sw.status == 0 {
				WriteError(sw, r, &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"})
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			p := recover()
			if p != nil {
				status = http.StatusInternalServerError
			}
			if status == 0 {
				status = http.StatusOK
			}
			if st := stateFrom(r.Context()); st != nil {
				st.log(r.Context(), slog.LevelInfo, "request",
					"method", r.Method, "path", r.URL.Path, "route", st.route, "status", status,
					"bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds(), "user_id", st.userID)
			}
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// corsMiddleware answers preflights and sets credentialed CORS headers for allowed origins. It
// also rejects state-changing requests from foreign origins, a CSRF backstop on top of
// SameSite=Lax session cookies.
func corsMiddleware(allowed []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			ok := slices.Contains(allowed, origin)
			if ok {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if !ok {
					WriteError(w, r, Forbidden("origin not allowed"))
					return
				}
				h := w.Header()
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
					h.Set("Access-Control-Allow-Headers", req)
				}
				h.Set("Access-Control-Max-Age", "600")
				h.Set("Access-Control-Expose-Headers", "X-Request-Id")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !ok && !safeMethod(r.Method) && !sameOrigin(origin, r.Host) {
				WriteError(w, r, Forbidden("cross-origin request rejected"))
				return
			}
			if ok {
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}

func chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// statusWriter records the status and size of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush supports streaming handlers that type-assert http.Flusher.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
