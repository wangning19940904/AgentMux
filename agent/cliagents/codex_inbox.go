package cliagents

import (
	"fmt"
	"sync"
)

// RPC replies and notifications share one reader. Notifications must never
// block that reader while a turn is waiting for its turn/start RPC reply.
// Each turn gets its own inbox; idle sessions retain no late notifications.
type codexInbox struct {
	mu    sync.Mutex
	queue []map[string]any
	err   error
	ready chan struct{}
}

const codexInboxLimit = 4096

func newCodexInbox() *codexInbox {
	return &codexInbox{ready: make(chan struct{}, 1)}
}

func (q *codexInbox) push(message map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return
	}
	if len(q.queue) >= codexInboxLimit {
		// Fail this turn explicitly instead of losing events or blocking every
		// session on the shared app-server connection.
		q.err = fmt.Errorf("app-server notification backlog exceeded %d events", codexInboxLimit)
		q.queue = nil
	} else {
		q.queue = append(q.queue, message)
	}
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *codexInbox) pop() (map[string]any, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return nil, q.err
	}
	if len(q.queue) == 0 {
		return nil, nil
	}
	message := q.queue[0]
	q.queue[0] = nil
	q.queue = q.queue[1:]
	if len(q.queue) > 0 {
		select {
		case q.ready <- struct{}{}:
		default:
		}
	}
	return message, nil
}
