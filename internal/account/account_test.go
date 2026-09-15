package account

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/manuelfritz/mail-bot/internal/config"
	"github.com/manuelfritz/mail-bot/internal/notify"
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

// recorder is a Notifier that keeps every message it was handed, so a test
// can assert on what an operator would have received.
type recorder struct {
	messages []string
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Notify(_ context.Context, ev notify.Event) error {
	r.messages = append(r.messages, ev.Message)
	return nil
}

func testWorker() (*Worker, *recorder) {
	rec := &recorder{}
	acc := config.Account{Name: "gmx", Notifiers: []string{"rec"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(acc, map[string]notify.Notifier{"rec": rec}, time.Minute, false, log), rec
}

// offlineFor backdates the current failure run so the worker sees itself as
// having been offline for d, without a test having to wait that long.
func offlineFor(w *Worker, d time.Duration) {
	w.failingSince = time.Now().Add(-d)
}

var errDropped = errors.New("search unseen: use of closed network connection")

// The GMX session limit: hours of healthy polling, a drop, an immediate
// reconnect. Repeated over a day that must produce no notifications at all.
func TestExpectedDropCycleNotifiesNothing(t *testing.T) {
	w, rec := testWorker()
	for i := 0; i < 8; i++ {
		w.noteConnectionFailed(context.Background(), errDropped, time.Second)
		w.noteConnectionHealthy(context.Background())
	}
	if len(rec.messages) != 0 {
		t.Errorf("drop-and-recover cycles notified %v, want nothing", rec.messages)
	}
}

func TestConnectionFailureNotifiesOnceAfterTheAlertDelay(t *testing.T) {
	w, rec := testWorker()
	ctx := context.Background()

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	if len(rec.messages) != 0 {
		t.Fatalf("first failure notified %v, want nothing", rec.messages)
	}

	// Still failing, but not for long enough yet.
	offlineFor(w, connectionAlertDelay-time.Second)
	w.noteConnectionFailed(ctx, errDropped, 64*time.Second)
	if len(rec.messages) != 0 {
		t.Fatalf("failure just under the alert delay notified %v, want nothing", rec.messages)
	}

	offlineFor(w, connectionAlertDelay+time.Second)
	w.noteConnectionFailed(ctx, errDropped, 128*time.Second)
	if len(rec.messages) != 1 {
		t.Fatalf("failure past the alert delay notified %v, want exactly one message", rec.messages)
	}

	// A problem that stays stuck keeps retrying but must not keep notifying.
	for i := 0; i < 5; i++ {
		w.noteConnectionFailed(ctx, errDropped, maxReconnectBackoff)
	}
	if len(rec.messages) != 1 {
		t.Fatalf("a stuck failure notified %v, want exactly one message", rec.messages)
	}

	w.noteConnectionHealthy(ctx)
	if len(rec.messages) != 2 {
		t.Fatalf("recovery notified %v, want a second message", rec.messages)
	}
}

// A recovery is only news if the failure was ever reported, and after one
// the worker has to be ready to report the next failure from scratch.
func TestConnectionRecoveryResetsTheFailureRun(t *testing.T) {
	w, rec := testWorker()
	ctx := context.Background()

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	w.noteConnectionHealthy(ctx)
	if len(rec.messages) != 0 {
		t.Fatalf("recovery from an unreported failure notified %v, want nothing", rec.messages)
	}
	if !w.failingSince.IsZero() {
		t.Error("failure run still open after recovery")
	}

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	offlineFor(w, connectionAlertDelay+time.Second)
	w.noteConnectionFailed(ctx, errDropped, 128*time.Second)
	if len(rec.messages) != 1 {
		t.Errorf("a later, longer failure notified %v, want one message", rec.messages)
	}
}
