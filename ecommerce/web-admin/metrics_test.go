package main

import (
	"bufio"
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
