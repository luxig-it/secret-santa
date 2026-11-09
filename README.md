# Secret Santa

A small self-hosted app for organising a Secret Santa ("Wichteln"). One Go
binary, one SQLite file, configuration through environment variables only.
The interface is German.

## How it works

1. **Organizer** – logs in with an organizer key, creates a campaign (title,
   optional budget and planned draw date) and shares the join link.
2. **Joining** – everybody opens the join link, enters their name and gets a
   private link. There are no accounts; the link *is* the login.
3. **Wishes** – everybody enters a wish in a small Markdown editor (toolbar for
   bold, italic, lists and links, with a preview).
4. **Surprises** – with a switch, each participant decides whether others may
   leave *surprise ideas* for them. Anybody may leave an idea for anybody who
   accepts them, regardless of who will draw that person.
   - Only the author can read an idea before the draw.
   - The recipient and everybody else only see *that* ideas exist.
   - The recipient never sees the ideas left for them.
5. **Group overview** – everybody in the group sees all members and their
   status: wish entered, number of surprise ideas, ready for the draw.
6. **Draw** – a participant is ready once they entered a wish, unless they
   switched on *Bedenkzeit* ("time to think") while saving it. When everybody
   is ready the organizer draws. The result is a single cycle (nobody draws
   themselves, no pairs that only gift each other), honouring the organizer's
   exclusions.
7. **Result** – everybody now sees whom they give to, that person's wish and
   every surprise idea left for them. After the draw nothing can be changed.

The organizer page deliberately shows neither wishes, ideas nor the
assignment. The organizer can take part by joining through the join link
like everybody else.

## Running

```sh
make watch   # templ regeneration and live reload on :8080
make run     # start once
make test    # go test ./...
make image   # production container
```

With the devcontainer, `.env` is written on first start and the organizer key
is `dev-organizer-token`.

```sh
docker run -p 8080:8080 -v secret-santa:/data \
  -e SECRET_SANTA_BASE_URL=https://wichteln.example.com \
  -e SECRET_SANTA_ORGANIZER_TOKENS=carsten:a-long-secret \
  ghcr.io/luxig-it/secret-santa:dev
```

See [`.env.example`](.env.example) for every setting. Organizer labels must
be unique, because campaigns are filed under them; renaming a label detaches
that organizer's existing campaigns. Without
`SECRET_SANTA_ORGANIZER_TOKENS` the app generates a key on start and prints
it. Mail delivery is not implemented yet; links are shown on the pages and
shared by hand.

## Layout

| Path | Content |
| --- | --- |
| `cmd/secret-santa` | `main`: configuration, server, graceful shutdown |
| `internal/config` | environment parsing |
| `internal/store` | SQLite schema and every query, including the visibility rules for surprise ideas |
| `internal/draw` | the cycle-based assignment |
| `internal/markdown` | sanitised Markdown rendering |
| `internal/web` | routes and handlers; `views/` holds the templ templates, `static/` CSS and JS |

## Security notes

- Tokens are 128 bit random values; pages answer unknown tokens with 404.
- Pages send `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and a
  strict CSP, because their URL is the secret.
- User text is rendered without raw HTML, with dangerous URL schemes removed
  and images turned into links.
- Failed organizer logins are limited to 10 per client address in 15 minutes.
  Behind a reverse proxy all clients share the proxy's address.
- A lost private link cannot be recovered yet; there is no mail and the
  organizer cannot see participants' links.
