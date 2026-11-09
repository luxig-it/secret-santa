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
    drew the recipient (`SurprisesForGiver`). The recipient never.
  - The organizer page shows no wishes, ideas or assignments.
- A participant is ready once they entered a wish and did not ask for more
  time to think (`needs_time`); the draw requires every participant to be
  ready. After the draw nothing changes any more.
- Every store write that depends on campaign state checks it inside its
  transaction (`requireOpen`); the DSN takes the write lock up front.
- Pages carry secrets in their URL: keep `Referrer-Policy: no-referrer`,
  `Cache-Control: no-store` and the CSP, and keep tokens out of logs.
- Schema changes to existing tables need an entry in `addedColumns` in
  `internal/store/store.go`; `CREATE TABLE IF NOT EXISTS` alone does not
  reach existing databases.
- Mail is not implemented yet; the SMTP settings are only validated.
