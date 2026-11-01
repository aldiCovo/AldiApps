package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nats-io/nats.go"
)

// fakeTokens treats "user-<id>" as a valid token.
type fakeTokens struct {
	expires time.Time
	err     error
}

func (f fakeTokens) Check(_ context.Context, token string) (int64, time.Time, error) {
	if f.err != nil {
		return 0, time.Time{}, f.err
	}
	id, found := strings.CutPrefix(token, "user-")
	if !found {
		return 0, time.Time{}, errors.New("bad token")
	}
	userID, err := strconv.ParseInt(id, 10, 64)
	return userID, f.expires, err
}

func newTestServer(t *testing.T, tokens tokenChecker) (*httptest.Server, *Hub) {
	t.Helper()
	hub := newHub()
	server := httptest.NewServer(newHandler(hub, tokens, func() bool { return true }))
	t.Cleanup(server.Close)
	return server, hub
}

func valid() fakeTokens {
	return fakeTokens{expires: time.Now().Add(time.Hour)}
}

func dial(t *testing.T, server *httptest.Server, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	options := &websocket.DialOptions{HTTPHeader: http.Header{}}
	if token != "" {
		options.HTTPHeader.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", options)
	if conn != nil {
		t.Cleanup(func() { conn.CloseNow() })
	}
	return conn, resp, err
}

func mustDial(t *testing.T, server *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := dial(t, server, token)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func read(t *testing.T, conn *websocket.Conn, wait time.Duration) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, data, err := conn.Read(ctx)
	return string(data), err
}

func waitForConnections(t *testing.T, hub *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		total := hub.total
		hub.mu.Unlock()
		if total == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connections never reached %d", want)
}

func TestRefusesConnectionsWithoutAValidToken(t *testing.T) {
	server, hub := newTestServer(t, valid())
	for _, token := range []string{"", "not-a-user"} {
		_, resp, err := dial(t, server, token)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: want 401, got %v, %v", token, resp, err)
		}
	}
	waitForConnections(t, hub, 0)
}

func TestAnswers503WhileKeysAreUnavailable(t *testing.T) {
	server, _ := newTestServer(t, fakeTokens{err: errKeysUnavailable})
	_, resp, err := dial(t, server, "user-1")
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %v, %v", resp, err)
	}
}

func TestSignalsOnlyReachTheirOwner(t *testing.T) {
	server, hub := newTestServer(t, valid())
	owner := mustDial(t, server, "user-1")
	secondTab := mustDial(t, server, "user-1")
	other := mustDial(t, server, "user-2")
	waitForConnections(t, hub, 3)

	hub.Deliver(1, []byte(`{"order_id":7,"status":"paid"}`))
	for _, conn := range []*websocket.Conn{owner, secondTab} {
		got, err := read(t, conn, 2*time.Second)
		if err != nil || got != `{"order_id":7,"status":"paid"}` {
			t.Errorf("owner read %q, %v", got, err)
		}
	}
	if got, err := read(t, other, 200*time.Millisecond); err == nil {
		t.Errorf("another user received %q", got)
	}
}

func TestLimitsConnectionsPerUser(t *testing.T) {
	server, hub := newTestServer(t, valid())
	for range maxPerUser {
		mustDial(t, server, "user-1")
	}
	_, resp, err := dial(t, server, "user-1")
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("want 429, got %v, %v", resp, err)
	}
	mustDial(t, server, "user-2")
	waitForConnections(t, hub, maxPerUser+1)
}

func TestLimitsConnectionsInTotal(t *testing.T) {
	hub := newHub()
	for i := range maxConnections {
		if _, err := hub.Add(int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hub.Add(int64(maxConnections)); !errors.Is(err, errHubFull) {
		t.Errorf("want errHubFull, got %v", err)
	}
}

func TestClosesWhenTheTokenExpires(t *testing.T) {
	server, hub := newTestServer(t, fakeTokens{expires: time.Now().Add(300 * time.Millisecond)})
	conn := mustDial(t, server, "user-1")
	_, err := read(t, conn, 3*time.Second)
	if code := websocket.CloseStatus(err); code != websocket.StatusPolicyViolation {
		t.Errorf("want close code 1008, got %v (%v)", code, err)
	}
	waitForConnections(t, hub, 0)
}

func TestAConnectionThatFallsBehindIsEnded(t *testing.T) {
	hub := newHub()
	client, err := hub.Add(1)
	if err != nil {
		t.Fatal(err)
	}
	for range sendBuffer + 1 {
		hub.Deliver(1, []byte("{}"))
	}
	select {
	case <-client.stop:
	default:
		t.Error("the connection wasn't ended")
	}
	// Ending twice must not panic.
	hub.Deliver(1, []byte("{}"))
	hub.CloseAll()
}

func TestCloseAllEndsConnections(t *testing.T) {
	server, hub := newTestServer(t, valid())
	conn := mustDial(t, server, "user-1")
	waitForConnections(t, hub, 1)
	hub.CloseAll()
	_, err := read(t, conn, 3*time.Second)
	if code := websocket.CloseStatus(err); code != websocket.StatusTryAgainLater {
		t.Errorf("want close code 1013, got %v (%v)", code, err)
	}
	hub.Wait()
}

func TestRemovingTheLastConnectionForgetsTheUser(t *testing.T) {
	hub := newHub()
	client, err := hub.Add(1)
	if err != nil {
		t.Fatal(err)
	}
	hub.Remove(1, client)
	if len(hub.byUser) != 0 || hub.total != 0 {
		t.Errorf("left behind %v, total %d", hub.byUser, hub.total)
	}
}

func TestReadyzFollowsTheConnection(t *testing.T) {
	ready := false
	server := httptest.NewServer(newHandler(newHub(), valid(), func() bool { return ready }))
	defer server.Close()
	status := func() int {
		resp, err := http.Get(server.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", got)
	}
	ready = true
	if got := status(); got != http.StatusOK {
		t.Errorf("want 200, got %d", got)
	}
}

func TestSignalsArriveFromNATS(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL is not set")
	}
	server, hub := newTestServer(t, valid())
	listener, err := listen(url, hub)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.Flush(); err != nil {
		t.Fatal(err)
	}
	conn := mustDial(t, server, "user-42")
	waitForConnections(t, hub, 1)

	sender, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if err := sender.Publish("orders.42", []byte(`{"order_id":1,"status":"paid"}`)); err != nil {
		t.Fatal(err)
	}
	if err := sender.Publish("orders.not-a-number", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got, err := read(t, conn, 3*time.Second)
	if err != nil || got != `{"order_id":1,"status":"paid"}` {
		t.Errorf("read %q, %v", got, err)
	}
}
