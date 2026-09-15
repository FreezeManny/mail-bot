# mail-bot

A small Go service that watches IMAP inboxes and sorts incoming mail into
folders by sender, with Telegram notifications. Replaces the n8n "Email
Sorter" workflow. (The original `EmailSorter.json` export is gitignored and
so isn't part of the repo - it only exists in the working copy it was
exported into.)

## How it works

- One worker per account: connect, check `INBOX` for unseen mail, sort
  matches, then wait `poll_interval_seconds` and repeat. Reconnects with
  exponential backoff on any connection error, starting over from one
  second whenever the connection that dropped had been up and working -
  some providers (GMX) end a healthy session after a few hours, and that
  must not ratchet the delay up over a day of uptime. (IMAP `IDLE` for
  near-instant sorting was tried and dropped for now to keep this simple -
  may come back later.)
- Read/unread state is never touched. Messages are found with a `SEARCH` on
  `\Unseen` and read with an `ENVELOPE`-only `FETCH`; neither marks mail
  `\Seen`, so nothing the service looks at appears opened afterwards. Each
  worker instead remembers in memory which UIDs it has already examined, so
  unread mail that matches no rule isn't re-fetched on every poll. That
  memory is pruned to the mailbox's current unseen set each cycle, and
  dropped if `UIDVALIDITY` changes; losing it on restart just costs one
  extra `FETCH` per message.
- Folder rules and notifiers are defined in `config.yaml` (see
  `config.example.yaml`) — no code changes needed to add/edit rules. An
  account's `notifiers:` list is optional and defaults to every notifier
  declared in the top-level `notifiers:` list; set it explicitly to use
  only some, or to `[]` to opt an account out of notifications entirely.
- Mail is never lost, only ever relocated. Native `MOVE` (RFC 6851) is used
  when the server supports it - one atomic server-side operation. Otherwise
  the message is copied and the copy is confirmed present in the
  destination *before* the source is flagged `\Deleted`, and confirmation
  is always a positive statement from the server about the destination
  mailbox, never just the absence of an error: with `UIDPLUS` it's the
  `COPYUID` reply naming the new message, and without `UIDPLUS` it's the
  destination's `STATUS` message count having gone up. If the copy can't be
  confirmed, nothing is deleted and the message is retried next cycle. The
  expunge is scoped to that single message (`UID EXPUNGE`, RFC 4315), and
  is skipped entirely on servers without `UIDPLUS` rather than risking a
  blanket `EXPUNGE`. See `internal/imapops` for details.
- Notification channels are pluggable (`internal/notify`): Telegram ships
  today, more can be added by implementing the `Notifier` interface and
  registering a factory — no changes to the sorting logic. An account can
  list more than one notifier to fan out to multiple channels.
- Failures notify actively instead of sitting in a passive status field:
  each account tracks whether it's currently in a connection or
  message-processing failure, and fires a notification (through the same
  pluggable `notify.Notifier`s used for successful sorts) only on the
  *transition* into or out of failure - once when it breaks, once when it
  recovers - rather than on every retry or every poll cycle a stuck problem
  keeps recurring. Connection failures additionally have to survive
  `connectionAlertDelay` (2 minutes) before they notify at all, so neither
  the routine drop-and-reconnect of a provider-enforced session limit nor a
  minute of flaky line becomes a notification nobody reads. See
  `internal/account`'s `noteConnectionFailed` / `processingFailing`
  handling.

## Setup

1. `cp config.example.yaml config.yaml` and adjust accounts/folder rules.
2. `cp .env.example .env` and fill in the IMAP passwords (reuse the same
   app-specific passwords already set up for the n8n IMAP credentials) and
   the Telegram bot token/chat ID.
3. `docker compose up -d --build`
4. Check logs: `docker compose logs -f`

To validate rule changes safely before they take effect, run once with
`DRY_RUN=true` in `.env` (or `docker compose run -e DRY_RUN=true mailsorter`)
— it logs what it would move/notify without touching any mailbox. A
`DRY_RUN` value that isn't a boolean is a startup error rather than a
fallback to a live run, so a typo can't quietly start moving mail.

Misconfiguration fails at startup, not silently at runtime: unknown or
duplicated notifier names, a folder rule with no `folder`, and a rule with
neither `exact:` nor `domains:` (the usual `domain:` typo, which could
never match anything) all stop the service with an error naming the
offending entry.

Logs go to stdout only (`docker compose logs -f`), as structured JSON;
`LOG_LEVEL` (default `info`) controls verbosity. `docker`'s own
`json-file` log driver already handles rotation/retention, so there's no
in-app log file to manage.

## Development

[just](https://github.com/casey/just) drives the same recipes CI runs, so a
green pipeline is reproducible with one command locally:

```
just check          # go vet + go build   (CI runs this)
just format         # gofmt -w .
just format-check   # fail if anything is unformatted   (CI runs this)
just test           # go test ./...
just build          # check, then build ./mailsorter
just docker-build   # the image CI builds, minus the push
```

## Releases

Releases are automated with
[release-please](https://github.com/googleapis/release-please). Commit messages
follow [Conventional Commits](https://www.conventionalcommits.org/) and a PR
title check enforces it.

- Merging to `main` updates a release PR with the changelog and version bump.
- Merging that PR tags the release.
- Publishing the release builds and pushes the multi-arch container image to
  `ghcr.io/freezemanny/mail-bot` — the image is **not** pushed by hand, and
  `latest` only ever moves on a published release.

To run a released image instead of building locally, point `docker-compose.yml`
at `image: ghcr.io/freezemanny/mail-bot:latest` in place of `build: .`.

`internal/rules`, `internal/config`, `internal/notify` and
`internal/account` (reconnect backoff and the connection-alert policy) have
unit tests - including a regression test that the Telegram bot token (which sits in the
request URL, and which `net/http` embeds in every transport error) never
reaches a returned error and therefore never reaches the logs. The
`imapops` move/copy/expunge logic was additionally verified by hand against
a local GreenMail test server during development (not part of the shipped
repo).
