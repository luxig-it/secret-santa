# Secret Santa

Self-hosted Secret Santa ("Wichteln") app. See README.md for the product
rules; they are the spec.

## Conventions

- Code, comments, commits and PRs in English. The user interface is German.
- Work in small stacked PRs and run a code review before opening each one.
- `make test` runs templ generation and `go test ./...`. Generated
  `*_templ.go` files are not committed.
- Check UI changes in the browser via the Playwright MCP server, including
  a phone-width viewport and dark mode.

## Invariants that must not break

- Who may read what is decided in `internal/store` and the handlers in
  `internal/web`, never in templates:
  - Wishes of others: nobody before the draw; afterwards only the giver.
  - Surprise ideas: only the author before the draw; afterwards only whoever
    drew the recipient (`SurprisesForGiver`), and only if the recipient still
    accepts surprises. The recipient never.
  - The organizer page shows no wishes, ideas or assignments. It does offer
    each participant's personal link to copy, for lost links; the join page
    and other participants' pages never show anybody else's link.
- `HasWish()` means the wish *counts*: text is there and `surprise_suffices`
  is off; with it on, the text is kept but ignored everywhere, the giver's
  view included. `HasWishText()` is only for "is anything written".
- Readiness is `Participant.ReadyWith(surpriseCount)`: with
  `surprise_suffices`, at least one surprise idea; otherwise a counting wish
  and no `needs_time`. The
  draw checks it inside its transaction; views use `Member.Ready()`. After the draw nothing changes any more – the one exception is
  the organizer's greeting (`SetGreeting`), which touches no assignment.
- Every store write that depends on campaign state checks it inside its
  transaction (`requireOpen`); the DSN takes the write lock up front.
- Pages carry secrets in their URL: keep `Referrer-Policy: no-referrer`,
  `Cache-Control: no-store` and the CSP, and keep tokens out of logs.
- Schema changes to existing tables need an entry in `addedColumns` in
  `internal/store/store.go`; `CREATE TABLE IF NOT EXISTS` alone does not
  reach existing databases.
- Mail is optional (`SECRET_SANTA_ALLOW_EMAIL`): it only ever sends a
  link to an address the person typed in, never stores the address, is
  rate-limited per link and per IP, and never logs in without STARTTLS.
  Handlers get a `mail.Sender`, nil when off; tests use a fake.
