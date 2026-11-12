package main

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// Notifier tells realtime-service that a customer has a new message. A lost signal only delays
// it, because the browser also asks now and then, so a NATS outage never stops a message.
type Notifier struct {
	conn *nats.Conn
}

func newNotifier(url string) (*Notifier, error) {
	conn, err := nats.Connect(url,
		nats.Name("chat-service"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.IgnoreAuthErrorAbort(),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				slog.Warn("disconnected from NATS", "error", err)
			}
		}),
		nats.ConnectHandler(func(*nats.Conn) { slog.Info("connected to NATS") }),
		nats.ReconnectHandler(func(*nats.Conn) { slog.Info("reconnected to NATS") }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			slog.Error("NATS error", "error", err)
		}),
	)
	if err != nil {
		return nil, err
	}
	return &Notifier{conn: conn}, nil
}

// Message publishes to the customer's subject, so realtime-service can tell whose chat it is.
func (n *Notifier) Message(userID, messageID int64) {
	payload, err := json.Marshal(struct {
		Type      string `json:"type"`
		MessageID int64  `json:"message_id"`
	}{"chat", messageID})
	if err != nil {
		slog.Error("encoding a signal failed", "error", err)
		return
	}
	if err := n.conn.Publish("chat."+strconv.FormatInt(userID, 10), payload); err != nil {
		slog.Warn("sending a signal failed", "user", userID, "error", err)
	}
}

func (n *Notifier) Close() {
	n.conn.Close()
}
