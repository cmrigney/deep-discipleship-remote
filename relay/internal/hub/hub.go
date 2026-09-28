// Package hub routes messages between authenticated clients. A single goroutine
// owns all hub state; clients talk to it over channels.
package hub

import (
	"context"

	"ddremote/relay/internal/protocol"
)

// SendBuffer is the per-client outbound queue size. A client that falls this
// far behind is disconnected rather than allowed to block the hub.
const SendBuffer = 16

// Client is one authenticated connection.
type Client struct {
	Role protocol.Role
	Name string
	IP   string

	send chan []byte
	// kick is called (from the hub goroutine) when the client can't keep up.
	kick func()
}

// NewClient creates a client. kick must be safe to call more than once and must not block.
func NewClient(role protocol.Role, name, ip string, kick func()) *Client {
	return &Client{Role: role, Name: name, IP: ip, send: make(chan []byte, SendBuffer), kick: kick}
}

// Send returns the channel of outbound messages for this client's writer.
func (c *Client) Send() <-chan []byte { return c.send }

type inbound struct {
	from *Client
	typ  string
	data []byte
}

// Hub is the message router.
type Hub struct {
	register   chan *Client
	unregister chan *Client
	inbound    chan inbound
	direct     chan inbound

	// Owned by the Run goroutine.
	players     map[*Client]bool
	controllers map[*Client]bool
	lastState   []byte
	// membershipChanged is set when a client is dropped, so presence is re-sent.
	membershipChanged bool
}

// New creates a hub. Call Run to start it.
func New() *Hub {
	return &Hub{
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		inbound:     make(chan inbound, 64),
		direct:      make(chan inbound, 64),
		players:     make(map[*Client]bool),
		controllers: make(map[*Client]bool),
	}
}

// Register adds a client. It blocks until the hub has accepted it or ctx ends.
func (h *Hub) Register(ctx context.Context, c *Client) bool {
	select {
	case h.register <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// Unregister removes a client and closes its send channel.
func (h *Hub) Unregister(ctx context.Context, c *Client) {
	select {
	case h.unregister <- c:
	case <-ctx.Done():
	}
}

// Route delivers a validated message from c according to its type.
func (h *Hub) Route(ctx context.Context, c *Client, typ string, data []byte) {
	select {
	case h.inbound <- inbound{from: c, typ: typ, data: data}:
	case <-ctx.Done():
	}
}

// Reply queues a message for c only (errors, pongs). It goes through the hub so
// it never races with the hub closing c's send channel.
func (h *Hub) Reply(ctx context.Context, c *Client, data []byte) {
	select {
	case h.direct <- inbound{from: c, data: data}:
	case <-ctx.Done():
	}
}

// Run processes hub events until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for c := range h.players {
				h.drop(c)
			}
			for c := range h.controllers {
				h.drop(c)
			}
			return

		case c := <-h.register:
			if c.Role == protocol.RolePlayer {
				h.players[c] = true
			} else {
				h.controllers[c] = true
				if h.lastState != nil {
					h.deliver(c, h.lastState)
				}
			}
			h.membershipChanged = true

		case c := <-h.unregister:
			h.drop(c)

		case m := <-h.direct:
			if h.players[m.from] || h.controllers[m.from] {
				h.deliver(m.from, m.data)
			}

		case m := <-h.inbound:
			switch m.from.Role {
			case protocol.RoleController:
				if !h.controllers[m.from] {
					continue
				}
				for p := range h.players {
					h.deliver(p, m.data)
				}
			case protocol.RolePlayer:
				if !h.players[m.from] {
					continue
				}
				if m.typ == protocol.TypeState {
					h.lastState = m.data
				}
				for c := range h.controllers {
					h.deliver(c, m.data)
				}
			}
		}

		// Presence can itself drop slow clients, so repeat until stable.
		for h.membershipChanged {
			h.membershipChanged = false
			if len(h.players) == 0 {
				h.lastState = nil
			}
			h.broadcastPresence()
		}
	}
}

func (h *Hub) broadcastPresence() {
	msg := protocol.Encode(protocol.Presence{
		Type:        protocol.TypePresence,
		Players:     len(h.players),
		Controllers: len(h.controllers),
	})
	for c := range h.controllers {
		h.deliver(c, msg)
	}
	for p := range h.players {
		h.deliver(p, msg)
	}
}

// deliver queues data for c without blocking; a full queue disconnects c.
func (h *Hub) deliver(c *Client, data []byte) {
	select {
	case c.send <- data:
	default:
		h.drop(c)
	}
}

// drop removes c from the hub, closes its queue and kicks the connection.
func (h *Hub) drop(c *Client) {
	if !h.players[c] && !h.controllers[c] {
		return
	}
	delete(h.players, c)
	delete(h.controllers, c)
	close(c.send)
	c.kick()
	h.membershipChanged = true
}
