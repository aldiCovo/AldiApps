package main

import (
	"errors"
	"sync"
)

const (
	// Enough for a few tabs and devices. One account can't hold every connection.
	maxPerUser     = 5
	maxConnections = 500
	sendBuffer     = 16
)

var (
	errUserFull = errors.New("too many connections for this user")
	errHubFull  = errors.New("too many connections")
)

// Client is one browser connection.
type Client struct {
	send chan []byte
	// stop closes when the connection must end, because it fell behind or the service is stopping.
	stop chan struct{}
	once sync.Once
}

func (c *Client) end() {
	c.once.Do(func() { close(c.stop) })
}

// Hub finds a user's connections, so a signal only reaches the user it belongs to.
type Hub struct {
	mu     sync.Mutex
	byUser map[int64]map[*Client]struct{}
	total  int
	active sync.WaitGroup
}

func newHub() *Hub {
	return &Hub{byUser: map[int64]map[*Client]struct{}{}}
}

func (h *Hub) Add(userID int64) (*Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= maxConnections {
		return nil, errHubFull
	}
	if len(h.byUser[userID]) >= maxPerUser {
		return nil, errUserFull
	}
	client := &Client{send: make(chan []byte, sendBuffer), stop: make(chan struct{})}
	if h.byUser[userID] == nil {
		h.byUser[userID] = map[*Client]struct{}{}
	}
	h.byUser[userID][client] = struct{}{}
	h.total++
	h.active.Add(1)
	return client, nil
}

func (h *Hub) Remove(userID int64, client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byUser[userID], client)
	if len(h.byUser[userID]) == 0 {
		delete(h.byUser, userID)
	}
	h.total--
	h.active.Done()
}

// Deliver never waits. A connection that fell behind is ended instead, because the browser
// loads the current status when it reconnects.
func (h *Hub) Deliver(userID int64, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.byUser[userID] {
		select {
		case client.send <- payload:
		default:
			client.end()
		}
	}
}

// CloseAll ends every connection, because http.Server.Shutdown doesn't track WebSockets.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, clients := range h.byUser {
		for client := range clients {
			client.end()
		}
	}
}

// Wait returns once every connection has ended.
func (h *Hub) Wait() {
	h.active.Wait()
}
