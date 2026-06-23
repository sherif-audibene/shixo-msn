package client

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/sherifhamad/shixo-msn/internal/proto"
)

// Subscribe connects the websocket, emits events on the returned channel, and
// auto-reconnects with backoff until ctx is canceled. The channel is closed on
// ctx cancel.
func Subscribe(ctx context.Context, cfg Config) <-chan proto.Event {
	out := make(chan proto.Event, 32)
	go func() {
		defer close(out)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			err := streamOnce(ctx, cfg, out)
			if ctx.Err() != nil {
				return
			}
			_ = err // logged by caller via GUI status; just back off and retry
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
	return out
}

func streamOnce(ctx context.Context, cfg Config, out chan<- proto.Event) error {
	u, err := url.Parse(strings.TrimRight(cfg.ServerURL, "/") + "/api/ws")
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set("token", cfg.Token)
	u.RawQuery = q.Encode()

	c, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	for {
		var ev proto.Event
		if err := wsjson.Read(ctx, c, &ev); err != nil {
			return err
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
