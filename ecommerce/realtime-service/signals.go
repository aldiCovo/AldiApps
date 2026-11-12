package main

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Order changes from order-service and new chat messages from chat-service, each on the
// subject of the customer they belong to.
var signalSubjects = []string{"orders.*", "chat.*"}

// listen passes order signals to the hub. Core NATS keeps nothing, so a signal sent while this
// is disconnected is lost, and browsers catch up through their slower polling.
func listen(url string, hub *Hub) (*nats.Conn, error) {
	conn, err := nats.Connect(url,
		nats.Name("realtime-service"),
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
	deliver := func(msg *nats.Msg) {
		_, id, found := strings.Cut(msg.Subject, ".")
		if !found {
			return
		}
		userID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			slog.Warn("ignoring a signal with a bad subject", "subject", msg.Subject)
			return
		}
		hub.Deliver(userID, msg.Data)
	}
	for _, subject := range signalSubjects {
		if _, err := conn.Subscribe(subject, deliver); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}
