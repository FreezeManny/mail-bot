package account

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/manuelfritz/mail-bot/internal/notify"
	"github.com/manuelfritz/mail-bot/internal/rules"
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

// recordingNotifier captures the events a Worker sends it.
type recordingNotifier struct {
	name   string
	events []notify.Event
}

func (r *recordingNotifier) Name() string { return r.name }

func (r *recordingNotifier) Notify(_ context.Context, ev notify.Event) error {
	r.events = append(r.events, ev)
	return nil
}

// messages is the status text of everything recorded, which is what an
// operator would actually have read.
func (r *recordingNotifier) messages() []string {
	var ms []string
	for _, ev := range r.events {
		ms = append(ms, ev.Message)
	}
	return ms
}

// A Worker is built from props alone - no config file, no env vars, no
// notifier registry - which is what makes this test possible at all.
func testWorker(p Props) (*Worker, *recordingNotifier) {
	n := &recordingNotifier{name: "recorder"}
	p.Name = "gmx"
	p.Notifiers = []notify.Notifier{n}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(p), n
}

func paypalMessage() *imapclient.FetchMessageBuffer {
	return &imapclient.FetchMessageBuffer{
		UID: 42,
		Envelope: &imap.Envelope{
			Subject: "Your receipt",
			From:    []imap.Address{{Mailbox: "service", Host: "paypal.de"}},
		},
	}
}

var paypalRules = []rules.FolderRule{{
	Folder:  "Sort/PayPal",
	Domains: []string{"paypal.de"},
}}

// Dry run stops before the session is ever touched, so passing a nil
// session here is also the assertion that nothing reached the network.
func TestProcessMessageDryRunMovesNothing(t *testing.T) {
	w, n := testWorker(Props{FolderRules: paypalRules, DryRun: true})

	if err := w.processMessage(context.Background(), nil, paypalMessage()); err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	if len(n.events) != 0 {
		t.Errorf("dry run sent %d notifications, want 0", len(n.events))
	}
}

// A sender no rule matches is left alone, again without touching the
// session.
func TestProcessMessageIgnoresUnmatchedSender(t *testing.T) {
	w, n := testWorker(Props{FolderRules: paypalRules})

	msg := paypalMessage()
	msg.Envelope.From = []imap.Address{{Mailbox: "someone", Host: "elsewhere.example"}}

	if err := w.processMessage(context.Background(), nil, msg); err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	if len(n.events) != 0 {
		t.Errorf("unmatched sender sent %d notifications, want 0", len(n.events))
	}
}

// Status updates go to the notifier instances in the props, tagged with the
// account name the props carry.
func TestNotifyUsesPropsNotifiers(t *testing.T) {
	w, n := testWorker(Props{FolderRules: paypalRules})

	w.notify(context.Background(), "connection restored")

	if len(n.events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(n.events))
	}
	if n.events[0].Account != "gmx" {
		t.Errorf("Account = %q, want %q", n.events[0].Account, "gmx")
	}
	if n.events[0].Message != "connection restored" {
		t.Errorf("Message = %q, want %q", n.events[0].Message, "connection restored")
	}
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
	w, rec := testWorker(Props{})
	for i := 0; i < 8; i++ {
		w.noteConnectionFailed(context.Background(), errDropped, time.Second)
		w.noteConnectionHealthy(context.Background())
	}
	if len(rec.messages()) != 0 {
		t.Errorf("drop-and-recover cycles notified %v, want nothing", rec.messages())
	}
}

func TestConnectionFailureNotifiesOnceAfterTheAlertDelay(t *testing.T) {
	w, rec := testWorker(Props{})
	ctx := context.Background()

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	if len(rec.messages()) != 0 {
		t.Fatalf("first failure notified %v, want nothing", rec.messages())
	}

	// Still failing, but not for long enough yet.
	offlineFor(w, connectionAlertDelay-time.Second)
	w.noteConnectionFailed(ctx, errDropped, 64*time.Second)
	if len(rec.messages()) != 0 {
		t.Fatalf("failure just under the alert delay notified %v, want nothing", rec.messages())
	}

	offlineFor(w, connectionAlertDelay+time.Second)
	w.noteConnectionFailed(ctx, errDropped, 128*time.Second)
	if len(rec.messages()) != 1 {
		t.Fatalf("failure past the alert delay notified %v, want exactly one message", rec.messages())
	}

	// A problem that stays stuck keeps retrying but must not keep notifying.
	for i := 0; i < 5; i++ {
		w.noteConnectionFailed(ctx, errDropped, maxReconnectBackoff)
	}
	if len(rec.messages()) != 1 {
		t.Fatalf("a stuck failure notified %v, want exactly one message", rec.messages())
	}

	w.noteConnectionHealthy(ctx)
	if len(rec.messages()) != 2 {
		t.Fatalf("recovery notified %v, want a second message", rec.messages())
	}
}

// A recovery is only news if the failure was ever reported, and after one
// the worker has to be ready to report the next failure from scratch.
func TestConnectionRecoveryResetsTheFailureRun(t *testing.T) {
	w, rec := testWorker(Props{})
	ctx := context.Background()

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	w.noteConnectionHealthy(ctx)
	if len(rec.messages()) != 0 {
		t.Fatalf("recovery from an unreported failure notified %v, want nothing", rec.messages())
	}
	if !w.failingSince.IsZero() {
		t.Error("failure run still open after recovery")
	}

	w.noteConnectionFailed(ctx, errDropped, time.Second)
	offlineFor(w, connectionAlertDelay+time.Second)
	w.noteConnectionFailed(ctx, errDropped, 128*time.Second)
	if len(rec.messages()) != 1 {
		t.Errorf("a later, longer failure notified %v, want one message", rec.messages())
	}
}
