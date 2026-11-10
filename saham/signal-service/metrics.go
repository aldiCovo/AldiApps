package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// This file is the same in every service. Keep the copies equal.

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Requests answered, by the route that matched and the status sent.",
	}, []string{"route", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time to answer a request, by the route that matched.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"route"})
)

// instrument counts every request by its ServeMux pattern, such as "GET /products/{id}",
// so the labels stay few whatever paths visitors try, and logs it.
func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		elapsed := time.Since(start)
		// The mux writes the matched pattern into the request it was given, which is this one.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		// A WebSocket lasts as long as the visitor stays, which would only blur the latency.
		if !recorder.hijacked {
			httpDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		}
		httpRequests.WithLabelValues(route, strconv.Itoa(recorder.status)).Inc()
		logRequest(r, route, recorder.status, elapsed)
	})
}

// maxLoggedPath keeps a scanner's very long paths from filling the logs.
const maxLoggedPath = 200

// logRequest writes one line per request, at ERROR when the answer is 5xx, so Loki's
// ErrorLogsSpike alert sees it. The kubelet's health checks come every few seconds and are
// left out. Only the path is logged, never the query, which can hold a search or a token.
func logRequest(r *http.Request, route string, status int, elapsed time.Duration) {
	if route == "GET /healthz" || route == "GET /readyz" {
		return
	}
	level := slog.LevelInfo
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	path := r.URL.Path
	if len(path) > maxLoggedPath {
		path = path[:maxLoggedPath] + "..."
	}
	slog.LogAttrs(context.Background(), level, "request",
		slog.String("method", r.Method),
		slog.String("path", path),
		slog.String("route", route),
		slog.Int("status", status),
		slog.Float64("ms", float64(elapsed.Microseconds())/1000),
	)
}

type statusRecorder struct {
	http.ResponseWriter
	status   int
	wrote    bool
	hijacked bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Flush and Hijack keep streaming and WebSockets working through the recorder.
func (s *statusRecorder) Flush() {
	if err := http.NewResponseController(s.ResponseWriter).Flush(); err != nil {
		slog.Debug("flush failed", "error", err)
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil {
		s.hijacked = true
		s.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// serveMetrics answers /metrics on METRICS_PORT, 9091 if unset. It's a port of its own,
// so no Service or Ingress that routes the main port can reach it.
func serveMetrics() (stop func()) {
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9091"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           metricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server stopped", "error", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}
