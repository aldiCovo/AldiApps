package main

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// Notifier tells realtime-service that an order changed. A lost signal only delays what a
// browser shows, because the status is in the database. So a NATS outage must not stop orders,
// and this never returns an error to the saga.
type Notifier struct {
	conn *nats.Conn
}

func newNotifier(url string) (*Notifier, error) {
	conn, err := nats.Connect(url,
		nats.Name("order-service"),
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

// Changed publishes to the owner's subject, so realtime-service can tell whose order it is.
func (n *Notifier) Changed(userID, orderID int64, status string) {
	payload, err := json.Marshal(struct {
		OrderID int64  `json:"order_id"`
		Status  string `json:"status"`
	}{orderID, status})
	if err != nil {
		slog.Error("encoding a signal failed", "error", err)
		return
	}
	if err := n.conn.Publish("orders."+strconv.FormatInt(userID, 10), payload); err != nil {
		slog.Warn("sending a signal failed", "order", orderID, "error", err)
	}
}

func (n *Notifier) Close() {
	n.conn.Close()
}
