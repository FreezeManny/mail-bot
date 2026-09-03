// Package imapops implements the two IMAP write operations the sorter
// needs, with an explicit never-delete-mail contract: nothing is ever
// removed from a source folder until the server has positively confirmed
// the message arrived in the destination folder. See MoveEmail for exactly
// what that confirmation is in each case.
package imapops

import (
	"errors"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// EnsureFolder creates mailbox if it doesn't already exist. A mailbox that
// is already there is not a failure, mirroring the original n8n
// CreateMailbox node's onError: continueRegularOutput.
func EnsureFolder(c *imapclient.Client, mailbox string) error {
	err := c.Create(mailbox, nil).Wait()
	if err == nil || isAlreadyExists(err) {
		return nil
	}

	// Not every server reports a duplicate CREATE with [ALREADYEXISTS] -
	// some use a different response code, some only human-readable text,
	// and some localize it - and misreading "it's already there" as a
	// failure would stop this folder's mail being sorted at all. So before
	// giving up, ask the server outright whether the mailbox exists. Costs
	// one round-trip, and only on the error path.
	if exists, listErr := folderExists(c, mailbox); listErr == nil && exists {
		return nil
	}
	return fmt.Errorf("imapops: create folder %q: %w", mailbox, err)
}

func isAlreadyExists(err error) bool {
	var status *imap.Error
	return errors.As(err, &status) && status.Code == imap.ResponseCodeAlreadyExists
}

func folderExists(c *imapclient.Client, mailbox string) (bool, error) {
	// mailbox doubles as the LIST pattern here; should it ever contain a
	// wildcard, the reply could name other mailboxes too, so compare the
	// returned names exactly rather than trusting the match.
	mailboxes, err := c.List("", mailbox, nil).Collect()
	if err != nil {
		return false, err
	}
	for _, m := range mailboxes {
		if m.Mailbox == mailbox {
			return true, nil
		}
	}
	return false, nil
}

// MoveEmail relocates the message identified by uid from the currently
// selected mailbox to mailbox.
//
// Mail is never lost, only ever relocated. Nothing is flagged \Deleted
// until the copy has been confirmed present in the destination, and the
// confirmation is always a positive statement from the server about the
// destination mailbox - never merely the absence of an error:
//   - When the server supports MOVE (RFC 6851), it is used directly: a
//     single atomic server-side operation, so there is no window in which
//     the source could be deleted independently of the copy.
//   - Otherwise the message is UID COPYed. With UIDPLUS (RFC 4315) the
//     server's COPYUID reply names the new message in the destination,
//     which is the confirmation. Without UIDPLUS there is no COPYUID, so
//     the destination's message count is read (STATUS) before and after
//     the copy and must have gone up. Either way, if the copy cannot be
//     confirmed nothing is deleted: the message is left untouched and
//     retried on the next cycle.
//   - The expunge that actually removes the \Deleted-flagged source
//     message is scoped to just this one UID via UID EXPUNGE (RFC 4315),
//     so it can never affect another message a mail client elsewhere had
//     already flagged \Deleted in the same mailbox. UID EXPUNGE needs
//     UIDPLUS; without it the expunge step is skipped entirely, leaving a
//     confirmed-copied, \Deleted-flagged duplicate in the source mailbox
//     rather than risking a blanket EXPUNGE. skippedExpunge reports this
//     case so the caller can log it.
func MoveEmail(c *imapclient.Client, uid imap.UID, mailbox string) (skippedExpunge bool, err error) {
	uidSet := imap.UIDSetNum(uid)

	if c.Caps().Has(imap.CapMove) {
		if _, err := c.Move(uidSet, mailbox).Wait(); err != nil {
			return false, fmt.Errorf("imapops: move uid %d to %q: %w", uid, mailbox, err)
		}
		return false, nil
	}

	// Without UIDPLUS the only confirmation available is the destination's
	// message count going up, so the baseline has to be read before the
	// copy.
	uidPlus := c.Caps().Has(imap.CapUIDPlus)
	var before uint32
	if !uidPlus {
		if before, err = messageCount(c, mailbox); err != nil {
			return false, fmt.Errorf("imapops: reading %q message count before copy of uid %d: %w", mailbox, uid, err)
		}
	}

	copyData, err := c.Copy(uidSet, mailbox).Wait()
	if err != nil {
		return false, fmt.Errorf("imapops: copy uid %d to %q: %w", uid, mailbox, err)
	}

	if err := confirmCopied(c, mailbox, copyData, uidPlus, before); err != nil {
		return false, fmt.Errorf("imapops: copy of uid %d to %q unconfirmed, source left in place: %w", uid, mailbox, err)
	}

	if err := c.Store(uidSet, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		return false, fmt.Errorf("imapops: flag uid %d \\Deleted after confirmed copy to %q: %w", uid, mailbox, err)
	}

	if !uidPlus {
		return true, nil
	}

	if _, err := c.UIDExpunge(uidSet).Collect(); err != nil {
		return false, fmt.Errorf("imapops: uid expunge %d after confirmed copy to %q: %w", uid, mailbox, err)
	}
	return false, nil
}

// confirmCopied establishes that the copy really is in mailbox. A tagged
// OK to COPY alone is not treated as proof: a server that answers OK while
// reporting no destination message has not stored anything we would be
// willing to delete the original for.
func confirmCopied(c *imapclient.Client, mailbox string, copyData *imap.CopyData, uidPlus bool, before uint32) error {
	if uidPlus {
		if copyData == nil {
			return errors.New("server advertises UIDPLUS but sent no COPYUID reply")
		}
		destUIDs, ok := copyData.DestUIDs.Nums()
		if !ok || len(destUIDs) == 0 {
			return errors.New("server's COPYUID reply names no destination message")
		}
		return nil
	}

	// Without COPYUID this cannot single out our message: it confirms that
	// the destination gained a message across the copy, which combined
	// with the tagged OK is as strong as the protocol allows here. Sort
	// destinations are only ever written to by this service, so a
	// concurrent delivery inflating the count is not a practical concern -
	// and erring towards "confirmed" only ever risks leaving a duplicate,
	// never deleting mail that wasn't copied.
	after, err := messageCount(c, mailbox)
	if err != nil {
		return fmt.Errorf("reading message count after copy: %w", err)
	}
	if after <= before {
		return fmt.Errorf("message count did not increase (%d -> %d)", before, after)
	}
	return nil
}

// messageCount reports how many messages mailbox holds, without selecting
// it (STATUS, RFC 9051 section 6.3.11) - the sorter has INBOX selected and
// must keep it that way.
func messageCount(c *imapclient.Client, mailbox string) (uint32, error) {
	data, err := c.Status(mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil {
		return 0, err
	}
	if data.NumMessages == nil {
		return 0, fmt.Errorf("server reported no message count for %q", mailbox)
	}
	return *data.NumMessages, nil
}
