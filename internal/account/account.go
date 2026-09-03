// Package account runs one IMAP account's sorting loop: connect, poll
// INBOX on an interval, match unseen messages against the account's folder
// rules, move matches, and notify.
//
// Read/unread state is never touched. Messages are located with a SEARCH
// on \Unseen and read with an ENVELOPE-only FETCH; neither sets \Seen, and
// nothing in this package stores a flag on a message it is only examining.
// Mail the user hasn't opened is still unopened after being sorted.
package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/manuelfritz/mail-bot/internal/config"
	"github.com/manuelfritz/mail-bot/internal/imapops"
	"github.com/manuelfritz/mail-bot/internal/notify"
	"github.com/manuelfritz/mail-bot/internal/rules"
)

const inboxMailbox = "INBOX"

const (
	initialReconnectBackoff = time.Second
	maxReconnectBackoff     = 5 * time.Minute
)

// Worker runs the sort loop for a single account.
type Worker struct {
	account      config.Account
	notifiers    []notify.Notifier
	pollInterval time.Duration
	dryRun       bool
	log          *slog.Logger

	// connectionFailing and processingFailing track whether we're currently
	// in a failure state for each concern, so a notification fires once on
	// the transition into/out of failure rather than on every retry or
	// every poll cycle a stuck problem keeps recurring.
	connectionFailing bool
	processingFailing bool

	// examined remembers which INBOX UIDs have already been looked at, so
	// an unread message matching no rule isn't re-fetched on every poll
	// cycle for as long as it stays unread. Since the sorter deliberately
	// never marks anything \Seen, this in-memory set is the only record
	// that a message was examined - it is pruned to the mailbox's current
	// unseen set on every poll so it can't grow without bound, and dropped
	// whenever UIDVALIDITY changes. Losing it on restart costs one extra
	// FETCH per message and nothing else.
	examined    map[imap.UID]bool
	uidValidity uint32
}

// New builds a Worker for acc, resolving its configured notifier names
// against the shared notifiers map.
func New(acc config.Account, notifiers map[string]notify.Notifier, pollInterval time.Duration, dryRun bool, log *slog.Logger) *Worker {
	var ns []notify.Notifier
	for _, name := range acc.Notifiers {
		if n, ok := notifiers[name]; ok {
			ns = append(ns, n)
		}
	}
	return &Worker{
		account:      acc,
		notifiers:    ns,
		pollInterval: pollInterval,
		dryRun:       dryRun,
		log:          log.With("account", acc.Name),
		examined:     make(map[imap.UID]bool),
	}
}

// session is one live IMAP connection, plus the state that only makes
// sense for that connection's lifetime.
type session struct {
	client *imapclient.Client

	// ensuredFolders remembers the destination folders already created or
	// confirmed present on this connection, so sorting a batch of messages
	// into one folder issues a single CREATE instead of one per message.
	// Scoped to the connection so a folder deleted behind our back is
	// re-checked after the next reconnect.
	ensuredFolders map[string]bool
}

func (s *session) ensureFolder(mailbox string) error {
	if s.ensuredFolders[mailbox] {
		return nil
	}
	if err := imapops.EnsureFolder(s.client, mailbox); err != nil {
		return err
	}
	s.ensuredFolders[mailbox] = true
	return nil
}

// Run connects and serves until ctx is canceled, reconnecting with backoff
// on any error. It never returns until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	backoff := initialReconnectBackoff
	for ctx.Err() == nil {
		err := w.connectAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.reportFailure(ctx, &w.connectionFailing, "connection", err)
			w.log.Error("connection lost, will reconnect", "error", err, "retry_in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxReconnectBackoff {
				backoff = maxReconnectBackoff
			}
			continue
		}
		backoff = initialReconnectBackoff
	}
}

func (w *Worker) connectAndServe(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", w.account.Host, w.account.Port)
	client, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer client.Close()

	// No imapclient call takes a context, so a server that accepts the
	// connection and then goes quiet would block a Wait() indefinitely -
	// past SIGTERM, until the container is killed. Closing the client on
	// cancellation unblocks whichever command is in flight with an I/O
	// error, which Run then sees as a shutdown. Close is safe to call
	// twice, so the defer above still stands.
	serving := make(chan struct{})
	defer close(serving)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-serving:
		}
	}()

	if err := client.Login(w.account.Username, w.account.Password()).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	selected, err := client.Select(inboxMailbox, nil).Wait()
	if err != nil {
		return fmt.Errorf("select %s: %w", inboxMailbox, err)
	}
	// A changed UIDVALIDITY means the server has renumbered the mailbox, so
	// every remembered UID may now refer to a different message.
	if selected.UIDValidity != w.uidValidity {
		w.uidValidity = selected.UIDValidity
		w.examined = make(map[imap.UID]bool)
	}

	s := &session{client: client, ensuredFolders: make(map[string]bool)}

	w.log.Info("connected")
	w.reportRecovery(ctx, &w.connectionFailing, "connection restored")

	if err := w.pollUnseen(ctx, s); err != nil {
		return err
	}

	return w.pollLoop(ctx, s)
}

// pollLoop keeps sorting on a plain interval until the connection errors
// out, at which point Run reconnects.
func (w *Worker) pollLoop(ctx context.Context, s *session) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.pollUnseen(ctx, s); err != nil {
				return err
			}
		}
	}
}

func (w *Worker) pollUnseen(ctx context.Context, s *session) error {
	data, err := s.client.UIDSearch(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}, nil).Wait()
	if err != nil {
		return fmt.Errorf("search unseen: %w", err)
	}

	// One FETCH for the whole batch rather than a round-trip per message.
	// ENVELOPE only: a body fetch would set \Seen, this does not.
	var msgs []*imapclient.FetchMessageBuffer
	if todo := w.unexamined(data.AllUIDs()); len(todo) > 0 {
		msgs, err = s.client.Fetch(imap.UIDSetNum(todo...), &imap.FetchOptions{
			Envelope: true,
			UID:      true,
		}).Collect()
		if err != nil {
			return fmt.Errorf("fetch envelopes: %w", err)
		}
	}

	var firstErr error
	for _, msg := range msgs {
		if ctx.Err() != nil {
			return nil
		}
		err := w.processMessage(ctx, s, msg)
		if err == nil {
			w.examined[msg.UID] = true
			continue
		}
		// A NO/BAD status response is the server refusing this one
		// command, so the remaining messages can still be sorted. Anything
		// else means the connection itself is unusable and only a
		// reconnect will fix it - returning the error gets us there
		// instead of polling a dead connection until the next SEARCH
		// happens to fail.
		if isConnectionError(err) {
			return fmt.Errorf("uid %d: %w", msg.UID, err)
		}
		w.log.Error("processing message failed, left in place for retry", "uid", msg.UID, "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		w.reportFailure(ctx, &w.processingFailing, "message processing", firstErr)
	} else {
		w.reportRecovery(ctx, &w.processingFailing, "message processing recovered")
	}

	return nil
}

// unexamined returns the UIDs in unseen that haven't been examined yet, and
// prunes the examined set down to the UIDs still present in the mailbox so
// it tracks the current unseen set rather than growing forever.
func (w *Worker) unexamined(unseen []imap.UID) []imap.UID {
	stillPresent := make(map[imap.UID]bool, len(w.examined))
	var todo []imap.UID
	for _, uid := range unseen {
		if w.examined[uid] {
			stillPresent[uid] = true
			continue
		}
		todo = append(todo, uid)
	}
	w.examined = stillPresent
	return todo
}

// isConnectionError reports whether err means the IMAP connection is
// unusable, rather than the server having refused one command. A tagged
// NO/BAD status response (*imap.Error) is the server answering us, so the
// connection is fine; anything else - an I/O error, a closed connection -
// is not recoverable without reconnecting.
func isConnectionError(err error) bool {
	var status *imap.Error
	return !errors.As(err, &status)
}

// reportFailure notifies once on the transition into a failure state for
// the concern tracked by flag, so a stuck problem notifies once rather than
// on every recurrence.
func (w *Worker) reportFailure(ctx context.Context, flag *bool, label string, err error) {
	if *flag {
		return
	}
	*flag = true
	w.notify(ctx, fmt.Sprintf("%s failed: %v", label, err))
}

// reportRecovery notifies once on the transition out of a failure state for
// the concern tracked by flag.
func (w *Worker) reportRecovery(ctx context.Context, flag *bool, message string) {
	if !*flag {
		return
	}
	*flag = false
	w.notify(ctx, message)
}

func (w *Worker) processMessage(ctx context.Context, s *session, msg *imapclient.FetchMessageBuffer) error {
	if msg.Envelope == nil {
		return nil
	}

	sender := senderAddress(msg.Envelope)
	if sender == "" {
		return nil
	}

	folder, ok := rules.Match(sender, w.account.FolderRules)
	if !ok {
		return nil
	}

	if w.dryRun {
		w.log.Info("dry-run: would move message", "uid", msg.UID, "from", sender, "subject", msg.Envelope.Subject, "folder", folder)
		return nil
	}

	if err := s.ensureFolder(folder); err != nil {
		return err
	}

	skippedExpunge, err := imapops.MoveEmail(s.client, msg.UID, folder)
	if err != nil {
		return err
	}
	if skippedExpunge {
		w.log.Warn("message copied to destination but not expunged from source (server lacks UIDPLUS); left as a duplicate", "uid", msg.UID, "folder", folder)
	}

	w.log.Info("moved message", "uid", msg.UID, "from", sender, "subject", msg.Envelope.Subject, "folder", folder)

	w.notifyEvent(ctx, notify.Event{
		Account: w.account.Name,
		Subject: msg.Envelope.Subject,
		Sender:  sender,
		Folder:  folder,
		Time:    time.Now(),
	})

	return nil
}

// notify sends a freeform status update (a connection/processing
// failure-or-recovery transition) to every notifier configured for this
// account.
func (w *Worker) notify(ctx context.Context, message string) {
	w.notifyEvent(ctx, notify.Event{
		Account: w.account.Name,
		Message: message,
		Time:    time.Now(),
	})
}

func (w *Worker) notifyEvent(ctx context.Context, ev notify.Event) {
	for _, n := range w.notifiers {
		if err := n.Notify(ctx, ev); err != nil {
			w.log.Error("notifier failed", "notifier", n.Name(), "error", err)
		}
	}
}

func senderAddress(env *imap.Envelope) string {
	if len(env.From) == 0 {
		return ""
	}
	return env.From[0].Addr()
}
