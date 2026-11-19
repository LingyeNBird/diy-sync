package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"fdsync/internal/protocol"
)

// The server can't call a client (clients connect out, often from behind
// WSL's NAT). When the panel needs something from a client (a file's
// content, a delete on that side), the request waits here until the
// client's long poll picks it up; the client answers with a PUT.
// Content passes through memory only; nothing is stored.

const (
	answerTimeout = 20 * time.Second
	maxAnswer     = 16 << 20
)

var errNoAnswer = errors.New("the client did not answer (offline?)")

type answer struct {
	body []byte
	size int64 // full file size when the read was cut
	err  string
}

type brokerReq struct {
	req    protocol.Request
	device string
	done   chan answer
}

type broker struct {
	mu      sync.Mutex
	queue   map[string][]*brokerReq // device → waiting to be picked up
	waiting map[string]*brokerReq   // id → picked up, waiting for the answer
	wake    map[string]chan struct{}
}

func newBroker() *broker {
	return &broker{queue: map[string][]*brokerReq{}, waiting: map[string]*brokerReq{}, wake: map[string]chan struct{}{}}
}

func newID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ask queues a request for a device and waits for the answer.
func (b *broker) ask(ctx context.Context, device string, req protocol.Request) (answer, error) {
	req.ID = newID()
	br := &brokerReq{req: req, device: device, done: make(chan answer, 1)}
	b.mu.Lock()
	b.queue[device] = append(b.queue[device], br)
	b.waiting[req.ID] = br
	if ch := b.wake[device]; ch != nil {
		close(ch)
		delete(b.wake, device)
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiting, req.ID)
		q := b.queue[device]
		for i, x := range q {
			if x == br {
				b.queue[device] = append(q[:i:i], q[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}()
	t := time.NewTimer(answerTimeout)
	defer t.Stop()
	select {
	case a := <-br.done:
		if a.err != "" {
			return a, errors.New(a.err)
		}
		return a, nil
	case <-t.C:
		return answer{}, errNoAnswer
	case <-ctx.Done():
		return answer{}, ctx.Err()
	}
}

// take returns the device's queued requests, waiting up to wait for one.
func (b *broker) take(ctx context.Context, device string, wait time.Duration) []protocol.Request {
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		b.mu.Lock()
		q := b.queue[device]
		if len(q) > 0 {
			delete(b.queue, device)
			b.mu.Unlock()
			out := make([]protocol.Request, len(q))
			for i, x := range q {
				out[i] = x.req
			}
			return out
		}
		ch := b.wake[device]
		if ch == nil {
			ch = make(chan struct{})
			b.wake[device] = ch
		}
		b.mu.Unlock()
		select {
		case <-ch:
		case <-t.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// deliver hands a client's answer to the waiting request.
func (b *broker) deliver(device, id string, a answer) bool {
	b.mu.Lock()
	br := b.waiting[id]
	b.mu.Unlock()
	if br == nil || br.device != device {
		return false
	}
	select {
	case br.done <- a:
	default:
	}
	return true
}
