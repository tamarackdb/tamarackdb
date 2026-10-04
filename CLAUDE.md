# Documentation writing guidelines

These rules apply to all documentation in this repo: `README.md`, every
page under `docs/content/`, the home page template
`docs/layouts/home.html`, and prose in Go doc comments.

## Style

- Short sentences. Simple, direct English.
- Avoid avoidable technical jargon and business buzzwords (leverage,
  seamless, robust, best-in-class, synergy, etc.).
- Necessary technical terms stay (SQLite, WAL, MVCC, FIFO, goroutine,
  etc.): they are the real names of the things being described, not
  jargon to eliminate. Simplify sentence length and phrasing around them,
  not the technical precision itself.
- Never use an em-dash ("—"). Use a comma, colon, semicolon, parentheses,
  or a new sentence instead.
- Every sentence earns its place: it states a rule, a behavior the reader
  relies on, or the reason for a rule. No asides, no trivia, no
  comparisons with other products.
- Never restate the previous sentence in other words ("Marie is hungry.
  She is very hungry."). Say it once.

## No references to prior designs

Write the current design as if it had always been the only one. Never say
"unlike its predecessor," "the old X," "this replaces Y," or similar, in
code comments or docs. Describe the current thing on its own terms. If
historical rationale needs to be preserved, put it in a commit message or
PR description, not in the doc or comment itself.

## Documentation structure

Docs are split by subject, like a wiki. The central rule: **one fact, one
page**. Each fact has one owning page; every other page links to it
instead of repeating it. Before writing a rule, find its owning page.
Duplicated facts drift apart.

Sections, relative to `docs/content/docs/`:

- `quickstart.md`: a single page, the first entry, before Concepts, and the target of
  the home page's "Get Started" button. It runs an instance, writes events
  in a transaction, reads them back, and shows a commit refused by a stale
  read, with `curl`. It is the one
  exception to "one fact, one page": it shows working requests and their
  responses, and links to the owning pages for every rule. When the API
  changes, its examples change too.
- `concepts/`: what TamarackDB is and the ideas the rest builds on:
  overview and terms, transactions, the mental model (the hall picture), events and
  tags, the store ID, the Append Condition, projections and rebuilds.
- `http-api/`: the wire contract: conventions (connecting, request
  bodies, the store ID header), query grammar, `QUERY /events`, the
  transaction endpoints (`/tx`), `POST /write`, projection endpoints,
  `POST /reset`, errors, and an example: one order in an online store,
  call by call.
- `operations/`: running an instance: install (local, systemd, Docker),
  configuration (the only settings table), security, health check,
  observability (`GET /stats`), logs, backup (`tamarackdb-backup`: its
  settings, how a run works, restoring, scheduling), maintenance,
  development mode.
- `README.md`: kept short: logo, badges, a one-paragraph intro, a short
  feature list, a single link to the documentation site at
  <https://tamarackdb.github.io/> without listing its pages, a contributing
  note, and, after a horizontal rule, a closing paragraph in italics on
  the name. No config tables, no
  Docker examples, no build instructions live in the README itself.

The site serves two readers: whoever calls the API, and whoever runs an
instance. How the server works inside is not on the site: it lives in Go
comments, next to the code it explains (see below). The site never links
to it. A guarantee that comes from an internal mechanism is published as a
behavior, on the API or operations page it concerns. Building from
source, the tests, and the demo dataset are in `CONTRIBUTING.md`.

## The mental model

`concepts/mental-model.md` is a picture of the whole model, after
Pull-The-Plug Modeling: a hall with a board, a card cabinet, employees who
keep notebooks, a head clerk, and under-clerks, and the application's
people at the counter. Rules for it:

- Only people act or know things. The hall, the board, or the cabinet is
  never the subject of an action verb.
- The employees and the clerks are TamarackDB: what they do is the
  server's behavior. The people at the counter are the application and
  its client library: what they do is never TamarackDB's.
- No technical terms in parentheses: the picture stands on its own.
- In the picture, people make *writes*, never *commits*. Committing is
  what the SQLite transaction does inside one write.

## Page layout

- A page opens with one or two sentences on what it covers.
- Within a section: rules as a bulleted list, one rule per bullet, edge
  cases included. Keep the reason for a rule in a short "**Why.**"
  paragraph, in prose, when a rewrite might otherwise simplify it in the
  wrong place.
- Normative rules (what a client, a client library, or an application has
  to do) use the RFC 2119 key words: MUST,
  MUST NOT, SHOULD, SHOULD NOT, MAY. No SHALL, no RECOMMENDED.
  Descriptions of how the server works stay ordinary sentences. A page
  that uses the key words says so near the top, with a link to
  `/docs/concepts/overview/#key-words`.

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

## Documentation site

`docs/` is a Hugo site using the Doks theme, published to
<https://tamarackdb.github.io/> by `.github/workflows/docs.yml` on each
`v*` tag.

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
- Top-level sidebar entries are listed in
  `docs/config/_default/menus/menus.en.toml` (`sidebar_docs`). A new
  section or top-level page must be added there.
- Build locally from `docs/` with `npm ci` then `npm run dev`.

## Versioning context

TamarackDB is pre-1.0. Don't write migration guides or deprecation notices
for internal changes; breaking changes to schema, config shape, or
internal APIs are expected before v1.0.
