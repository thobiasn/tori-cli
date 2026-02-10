package agent

import (
	"testing"
	"time"
)

func TestHubPublishSubscribe(t *testing.T) {
	h := NewHub()
	sub, ch := h.Subscribe(TopicMetrics)
	defer h.Unsubscribe(TopicMetrics, sub)

	msg := "hello"
	h.Publish(TopicMetrics, msg)

	select {
	case got := <-ch:
		if got != msg {
			t.Errorf("got %v, want %v", got, msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestHubMultipleSubscribers(t *testing.T) {
	h := NewHub()
	sub1, ch1 := h.Subscribe(TopicMetrics)
	sub2, ch2 := h.Subscribe(TopicMetrics)
	defer h.Unsubscribe(TopicMetrics, sub1)
	defer h.Unsubscribe(TopicMetrics, sub2)

	h.Publish(TopicMetrics, "msg")

	for i, ch := range []<-chan any{ch1, ch2} {
		select {
		case got := <-ch:
			if got != "msg" {
				t.Errorf("subscriber %d: got %v, want msg", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

func TestHubUnsubscribe(t *testing.T) {
	h := NewHub()
	sub, ch := h.Subscribe(TopicMetrics)

	h.Unsubscribe(TopicMetrics, sub)

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}

	// Publishing after unsubscribe should not panic.
	h.Publish(TopicMetrics, "msg")
}

func TestHubTopicIsolation(t *testing.T) {
	h := NewHub()
	sub, ch := h.Subscribe(TopicMetrics)
	defer h.Unsubscribe(TopicMetrics, sub)

	// Publish to a different topic.
	h.Publish(TopicLogs, "log entry")

	// Metrics subscriber should not receive it.
	select {
	case msg := <-ch:
		t.Errorf("unexpected message on metrics topic: %v", msg)
	case <-time.After(50 * time.Millisecond):
		// Expected — no message.
	}
}

func TestHubSlowConsumerDrop(t *testing.T) {
	h := NewHub()
	sub, ch := h.Subscribe(TopicMetrics)
	defer h.Unsubscribe(TopicMetrics, sub)

	// Fill the buffer.
	for i := range subscriberBufSize + 10 {
		h.Publish(TopicMetrics, i)
	}

	// Should be able to drain exactly subscriberBufSize.
	count := 0
	for range subscriberBufSize {
		select {
		case <-ch:
			count++
		default:
		}
	}
	if count != subscriberBufSize {
		t.Errorf("drained %d messages, want %d", count, subscriberBufSize)
	}
}
