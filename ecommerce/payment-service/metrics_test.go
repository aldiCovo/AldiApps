package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// This file is the same in every service. Keep the copies equal.

func TestMetricsCountRequestsByPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	mux.HandleFunc("GET /fine", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	server := httptest.NewServer(instrument(mux))
	defer server.Close()

	before := testutil.ToFloat64(httpRequests.WithLabelValues("GET /things/{id}", "418"))
	beforeFine := testutil.ToFloat64(httpRequests.WithLabelValues("GET /fine", "200"))
	beforeMissing := testutil.ToFloat64(httpRequests.WithLabelValues("unmatched", "404"))
	for _, path := range []string{"/things/1", "/things/2", "/fine", "/nothing-here"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := testutil.ToFloat64(httpRequests.WithLabelValues("GET /things/{id}", "418")) - before; got != 2 {
		t.Errorf("counted %v requests to the pattern, want 2", got)
	}
	if got := testutil.ToFloat64(httpRequests.WithLabelValues("GET /fine", "200")) - beforeFine; got != 1 {
		t.Errorf("counted %v requests that only wrote a body, want 1 with status 200", got)
	}
	if got := testutil.ToFloat64(httpRequests.WithLabelValues("unmatched", "404")) - beforeMissing; got != 1 {
		t.Errorf("counted %v unmatched requests, want 1", got)
	}
}

func TestMetricsKeepHijackingWorking(t *testing.T) {
	handler := instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("the recorder hides http.Hijacker")
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		_ = rw.Flush()
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	resp, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
}

func TestMetricsEndpointServesTheCounters(t *testing.T) {
	instrument(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	exposed := httptest.NewRecorder()
	metricsHandler().ServeHTTP(exposed, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	scanner := bufio.NewScanner(exposed.Body)
	found := map[string]bool{}
	for scanner.Scan() {
		for _, name := range []string{"http_requests_total{", "http_request_duration_seconds_bucket{", "go_goroutines "} {
			if strings.HasPrefix(scanner.Text(), name) {
				found[name] = true
			}
		}
	}
	if len(found) != 3 {
		t.Fatalf("/metrics has only %v", found)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(previous)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	handler := instrument(mux)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
		httptest.NewRequest(http.MethodGet, "/things/7?token=secret", nil),
		httptest.NewRequest(http.MethodPost, "/broken", nil),
		httptest.NewRequest(http.MethodGet, "/"+strings.Repeat("a", 300), nil),
	} {
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	if strings.Contains(out.String(), "secret") {
		t.Error("the query was logged")
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		lines = append(lines, entry)
	}
	if len(lines) != 3 {
		t.Fatalf("logged %d lines, want 3 without the health check: %s", len(lines), out.String())
	}
	if l := lines[0]; l["level"] != "INFO" || l["msg"] != "request" || l["method"] != "GET" || l["path"] != "/things/7" ||
		l["route"] != "GET /things/{id}" || l["status"] != float64(200) {
		t.Errorf("first line %v", l)
	}
	if _, ok := lines[0]["ms"].(float64); !ok {
		t.Errorf("no duration in %v", lines[0])
	}
	if l := lines[1]; l["level"] != "ERROR" || l["status"] != float64(502) {
		t.Errorf("a 5xx answer logged as %v, want ERROR", l)
	}
	if l := lines[2]; l["route"] != "unmatched" || len(l["path"].(string)) != maxLoggedPath+3 {
		t.Errorf("long unmatched path logged as %v", l)
	}
}
