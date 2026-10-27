package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"api/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hub tracks the live websocket of each paired peer.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*conn
}

type conn struct {
	out    chan protocol.Message
	cancel context.CancelFunc
}

func NewHub() *Hub { return &Hub{conns: map[string]*conn{}} }

// Online reports whether the peer currently has a websocket open.
func (h *Hub) Online(peerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[peerID]
	return ok
}

// Why a Send failed.
var (
	ErrOffline = errors.New("peer is offline")
	ErrBusy    = errors.New("peer connection is busy; try again")
)

// Send queues a message for the peer. It returns ErrOffline when no socket is
// open and ErrBusy when the socket is open but its buffer is full.
func (h *Hub) Send(peerID string, m protocol.Message) error {
	h.mu.RLock()
	c := h.conns[peerID]
	h.mu.RUnlock()
	if c == nil {
		return ErrOffline
	}
	select {
	case c.out <- m:
		return nil
	default:
		return ErrBusy
	}
}

// serve pumps messages until the socket dies. A newer connection from the same
// peer replaces the old one.
func (h *Hub) serve(ctx context.Context, peerID string, ws *websocket.Conn, hello protocol.Message, onMessage func(protocol.Message), onConnect func()) {
	ctx, cancel := context.WithCancel(ctx)
	c := &conn{out: make(chan protocol.Message, 16), cancel: cancel}

	h.mu.Lock()
	if old := h.conns[peerID]; old != nil {
		old.cancel()
	}
	h.conns[peerID] = c
	h.mu.Unlock()
	defer func() {
		cancel()
		h.mu.Lock()
		if h.conns[peerID] == c {
			delete(h.conns, peerID)
		}
		h.mu.Unlock()
	}()

	c.out <- hello
	if onConnect != nil {
		go onConnect()
	}

	go func() { // reader
		defer cancel()
		for {
			var m protocol.Message
			if err := wsjson.Read(ctx, ws, &m); err != nil {
				return
			}
			if onMessage != nil {
				onMessage(m)
			}
		}
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			ws.Close(websocket.StatusNormalClosure, "")
			return
		case m := <-c.out:
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsjson.Write(wctx, ws, m)
			wcancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// Broadcast queues m for every connected peer and returns how many accepted it.
func (h *Hub) Broadcast(m protocol.Message) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, c := range h.conns {
		select {
		case c.out <- m:
			n++
		default:
		}
	}
	return n
}

// Disconnect closes the peer's websocket, if any.
func (h *Hub) Disconnect(peerID string) {
	h.mu.RLock()
	c := h.conns[peerID]
	h.mu.RUnlock()
	if c != nil {
		c.cancel()
	}
}
