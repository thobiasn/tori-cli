package agent

import "sync"

// Hub topics.
const (
	TopicMetrics = "metrics"
	TopicLogs    = "logs"
	TopicAlerts  = "alerts"
)

const subscriberBufSize = 64

// Hub is an in-process pub/sub fan-out for streaming data to connected clients.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	ch chan any
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		subs: map[string]map[*subscriber]struct{}{
			TopicMetrics: {},
			TopicLogs:    {},
			TopicAlerts:  {},
		},
	}
}

// Subscribe returns a buffered channel that receives messages for the given topic.
// The returned *subscriber is used to Unsubscribe later.
func (h *Hub) Subscribe(topic string) (*subscriber, <-chan any) {
	s := &subscriber{ch: make(chan any, subscriberBufSize)}
	h.mu.Lock()
	if h.subs[topic] == nil {
		h.subs[topic] = make(map[*subscriber]struct{})
	}
	h.subs[topic][s] = struct{}{}
	h.mu.Unlock()
	return s, s.ch
}

// Unsubscribe removes a subscriber from a topic and closes its channel.
func (h *Hub) Unsubscribe(topic string, s *subscriber) {
	h.mu.Lock()
	if subs, ok := h.subs[topic]; ok {
		if _, exists := subs[s]; exists {
			delete(subs, s)
			close(s.ch)
		}
	}
	h.mu.Unlock()
}

// Publish sends a message to all subscribers of the given topic.
// Non-blocking: if a subscriber's buffer is full, the message is dropped.
func (h *Hub) Publish(topic string, msg any) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for s := range h.subs[topic] {
		select {
		case s.ch <- msg:
		default:
			// Slow consumer, drop message.
		}
	}
}
