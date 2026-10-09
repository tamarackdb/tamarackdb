# Documentation writing guidelines

These rules apply to all documentation in this repo: `README.md`,
`CONTRIBUTING.md`, every page under `docs/content/`, the home page
template `docs/layouts/home.html`, and prose in Go comments.

## Style

- Short sentences. Simple, direct English.
- Avoid technical jargon that isn't needed, and business buzzwords
  (leverage, seamless, robust, best-in-class, synergy, etc.).
- Necessary technical terms stay (SQLite, WAL, MVCC, FIFO, goroutine,
  etc.): they are the real names of the things being described, not
  jargon to eliminate. Simplify sentence length and phrasing around them,
  not the technical precision itself.
- Never use an em-dash ("—"). Use a comma, colon, semicolon, parentheses,
  or a new sentence instead.
- Never chain clauses into one long sentence with a colon. "Its
  projections can be written in the same commit, which is optional: an
  application can also update them afterwards, from the events, and let
  them lag behind." is two or three sentences: "Writing its projections
  in the same commit is optional. An application can also update them
  later, from the events."
- Every sentence earns its place: it states what to do, or a behavior the
  reader relies on. No asides, no trivia, no comparisons with other
  products.
- Never restate the previous sentence in other words ("Marie is hungry.
  She is very hungry."). Say it once.

## No references to prior designs

Write the current design as if it had always been the only one. Never say
"unlike its predecessor," "the old X," "this replaces Y," or similar, in
code comments or docs. Describe the current thing on its own terms. If
historical rationale needs to be preserved, put it in a commit message or
PR description, not in the doc or comment itself.

## Documentation structure

The site serves two readers, in this order: whoever runs an instance
(operators), and whoever builds an application on it (developers).
Developers are sent first to a client library; the HTTP API is a
reference for whoever writes a client.

The doc follows the code: the code changes first, and the site is brought
up to date before the next `v*` tag. A fact is written where its reader
needs it, even if another page says it too. Keep pages short: document an
edge case only when it affects the reader in practice.

Pages, relative to `docs/content/docs/`, in sidebar order:

- `key-features.md`: the entry page. A bulleted list of the key aspects,
  like the README's feature list but more detailed, each with a link to
  the page that covers it.
- `quickstart.md`: the target of the home page's "Get Started" button.
  It starts the server with Docker, then, in a transaction, reads
  several events and writes a new one, with `curl`: the happy path only.
  It's an overview, not a script to run as is: its responses may show
  events the reader never wrote. When the API changes, its examples
  change too.
- `operations/`: running an instance.
  - `install.md`: local development, the binaries (a release download,
    or `make build`), systemd, Docker.
  - `configuration.md`: the only settings table.
  - `security.md`.
  - `backup.md`: `tamarackdb-backup` (settings, how a run works,
    restoring, scheduling) and importing a dump.
  - `monitoring.md`: health check, `GET /stats`, logs.
  - `maintenance.md`: maintenance and development mode.
- `development/`: building an application on TamarackDB.
  - `client-libraries.md`: first in the section; the available client
    libraries, with a link to each one's repository.
  - `concepts.md`: the ideas the API builds on, on one page: events and
    tags, the store ID, the Append Condition, transactions, projections.
  - `http-api.md`: the HTTP API reference, on one page.
  - `writing-a-client.md`: what a client of the protocol has to do, in
    brief. It is the only page that points to the repository and
    `go doc ./...` for how the server works inside.
- `README.md`: kept short: logo, a one-paragraph intro, badges, a short
  feature list, a single link to the documentation site at
  <https://tamarackdb.github.io/> without listing its pages, a
  contributing note, and, after a horizontal rule, a closing paragraph
  in italics on the name. No config tables, no Docker examples, no build
  instructions live in the README itself.

How the server works inside, and the reasons behind a behavior, are not on
the site: they live in Go comments, next to the code they explain (see
below). A guarantee that comes from an internal mechanism is published as
a behavior, on the page it concerns. Installing the binaries, from a
release or with `make build`, is on the Install page. The tests, the demo
dataset, and the details of the build are in `CONTRIBUTING.md`.

## Page layout

- A page opens with one or two sentences on what it covers.
- Within a section: rules and behaviors as a bulleted list, one per
  bullet.
- Write rules as ordinary sentences ("the client sends the ticket back").
  No RFC 2119 key words on the site.
- No "**Why.**" paragraphs: the doc says what to do and what the server
  does. A reader who wants the reason reads the code and its comments.
- A JSON request body in a `curl` example, or a query example, is
  indented with 2 spaces, never on one line. A tag stays on one line: a
  `{"name": ..., "value": ...}` object in a query, and the `identifiers`
  and `metadata` objects of an event. A list of strings stays on one line
  too (`"types": ["seat-reserved"]`). NDJSON responses stay one object
  per line.

  ```sh
  curl -X QUERY http://127.0.0.1:8085/events \
    -H "Content-Type: application/json" \
    -d '{
      "query": [
        {
          "types": ["course-defined"],
          "identifiers": [
            {"name": "courseId", "value": "c1"}
          ]
        }
      ]
    }'
  ```

## Server internals in Go comments

How the server works inside is documented in Go comments, never on the
site: a comment lives in the same diff as its code, so it follows its
changes.

- Each package has a `doc.go` holding its package comment: its role, how
  it works, and the reason for each design choice that a rewrite might
  undo. `go doc ./...` gives the role of every package.
- The SQLite schema is commented in the SQL itself, one comment per table
  and per index.
- A rule a rewrite of TamarackDB has to keep goes in the comment of the
  code it concerns, with the RFC 2119 key words.
- Design options that were set aside don't go in comments: before 1.0,
  they live in commit messages and PR descriptions.
- Before changing concurrent code, follow the recommendations in
  `CONTRIBUTING.md`, "Concurrent code".

## Documentation site

`docs/` is a Hugo site using the Hextra theme, imported as a Hugo module
(`docs/go.mod`), published to
<https://tamarackdb.github.io/> by `.github/workflows/docs.yml` on each
`v*` tag, or by hand (`gh workflow run docs.yml`).

- Every page starts with front matter: `title`, `description`, `slug`,
  `weight`. The page title comes from `title`; don't add a `#` heading.
  `description` is one sentence of 110 to 160 characters saying what the
  page covers: it's the page's meta description, and its entry in
  `llms.txt`. A section's `_index.md` has a `description` too.
- `llms.txt` and `llms-full.txt` are generated at build time from the
  pages, in sidebar order (`docs/layouts/index.llms.txt` and
  `index.llmsfull.txt`): never edit their output by hand.
- Link between pages with absolute site paths, e.g.
  `[Configuration](/docs/operations/configuration/#limits)`, never with
  `.md` file paths.
- The sidebar follows the folders under `docs/content/docs/`, ordered by
  `weight`. The top bar is `main` in
  `docs/config/_default/menus/menus.en.toml`; it links to the PHP
  client's repository.
- The home page is `docs/content/_index.md`, built with Hextra's hero and
  feature-grid shortcodes.
- Build locally from `docs/` with `hugo serve` (Hugo extended and Go).

## Versioning context

TamarackDB is pre-1.0. Don't write migration guides or deprecation notices
for internal changes; breaking changes to schema, config shape, or
internal APIs are expected before v1.0.
