package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newEventsGateway runs web in front of a fake realtime-service that says hello and records
// the handshake it received.
func newEventsGateway(t *testing.T) (*httptest.Server, chan *http.Request) {
	t.Helper()
	seen := make(chan *http.Request, 4)
	realtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"order_id":1}`)); err != nil {
			return
		}
		conn.Read(r.Context())
	}))
	t.Cleanup(realtime.Close)
	gateway := httptest.NewServer(newHandler(realtime.URL, realtime.URL, realtime.URL, realtime.URL, realtime.URL))
	t.Cleanup(gateway.Close)
	return gateway, seen
}

func dialEvents(t *testing.T, gateway *httptest.Server, origin, cookie string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if cookie != "" {
		header.Set("Cookie", "session="+cookie+"; other=browser-only")
	}
	header.Set("User-Agent", "browser-only")
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/api/events", &websocket.DialOptions{HTTPHeader: header})
	if conn != nil {
		t.Cleanup(func() { conn.CloseNow() })
	}
	return conn, resp, err
}

func TestEventsPassTheTokenAndNothingElseFromTheBrowser(t *testing.T) {
	gateway, seen := newEventsGateway(t)
	conn, _, err := dialEvents(t, gateway, gateway.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != `{"order_id":1}` {
		t.Fatalf("read %q, %v", data, err)
	}
	r := <-seen
	if r.URL.RequestURI() != "/ws" {
		t.Errorf("path = %q", r.URL.RequestURI())
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	for _, name := range []string{"Cookie", "Origin", "X-Forwarded-For", "X-Forwarded-Host"} {
		if got := r.Header.Get(name); got != "" {
			t.Errorf("%s reached the service: %q", name, got)
		}
	}
	if ua := r.Header.Get("User-Agent"); ua == "browser-only" {
		t.Errorf("User-Agent reached the service: %q", ua)
	}
}

func TestEventsRefuseOtherOrigins(t *testing.T) {
	gateway, seen := newEventsGateway(t)
	for _, origin := range []string{"", "https://evil.example", "null"} {
		_, resp, err := dialEvents(t, gateway, origin, testToken)
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: want 403, got %v, %v", origin, resp, err)
		}
	}
	if len(seen) != 0 {
		t.Errorf("%d handshakes reached the service", len(seen))
	}
}

func TestEventsNeedALogin(t *testing.T) {
	gateway, seen := newEventsGateway(t)
	_, resp, err := dialEvents(t, gateway, gateway.URL, "")
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %v, %v", resp, err)
	}
	if len(seen) != 0 {
		t.Errorf("%d handshakes reached the service", len(seen))
	}
}

func TestEventsAnswer502WhileTheServiceIsDown(t *testing.T) {
	gateway := httptest.NewServer(newHandler("http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1"))
	defer gateway.Close()
	_, resp, err := dialEvents(t, gateway, gateway.URL, testToken)
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Errorf("want 502, got %v, %v", resp, err)
	}
}
