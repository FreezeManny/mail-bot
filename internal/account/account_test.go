package account

import (
	"testing"
	"time"
)

func TestReconnectBackoffRamps(t *testing.T) {
	var b reconnectBackoff

	// Every attempt fails immediately, as it would against a server that is
	// down: the wait doubles and then holds at the maximum.
	wantSeconds := []int{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300, 300}
	for i, s := range wantSeconds {
		want := time.Duration(s) * time.Second
		if got := b.next(0); got != want {
			t.Fatalf("attempt %d: next(0) = %v, want %v", i+1, got, want)
		}
	}
}

func TestReconnectBackoffResetsAfterStableConnection(t *testing.T) {
	var b reconnectBackoff
	for i := 0; i < 10; i++ {
		b.next(0)
	}
	if got := b.next(stableConnection); got != initialReconnectBackoff {
		t.Errorf("next(%v) after a ramped-up backoff = %v, want %v", stableConnection, got, initialReconnectBackoff)
	}
}

// A session limit like GMX's - hours of healthy polling, then a drop - has
// to reconnect at once every time, not walk the backoff up over a day of
// uptime.
func TestReconnectBackoffStaysAtInitialForRepeatedLongSessions(t *testing.T) {
	var b reconnectBackoff
	for i := 0; i < 10; i++ {
		if got := b.next(3 * time.Hour); got != initialReconnectBackoff {
			t.Fatalf("drop %d after a 3h session: next = %v, want %v", i+1, got, initialReconnectBackoff)
		}
	}
}

// A connection that drops just short of the threshold is still a fast
// failure, so it has to keep backing off rather than reset.
func TestReconnectBackoffDoesNotResetBelowThreshold(t *testing.T) {
	var b reconnectBackoff
	b.next(0)
	if got := b.next(stableConnection - time.Millisecond); got != 2*time.Second {
		t.Errorf("next just below the stable threshold = %v, want %v", got, 2*time.Second)
	}
}
