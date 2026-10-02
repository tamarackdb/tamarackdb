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

- `concepts/`: what TamarackDB is and the ideas the rest builds on:
  overview and terms, transactions, the courtyard picture, events and
  tags, the store ID, the Append Condition, projections and rebuilds.
- `http-api/`: the wire contract: conventions (connecting, request
  bodies, the store ID header), query grammar, `QUERY /events`,
  `POST /write`, projection endpoints, `POST /reset`, errors.
- `client-libraries.md`: what a client library must do on top of the
  HTTP API.
- `server-internals/`: how the server works inside: the write FIFO, the
  Sequence Position counter, reads, lifecycle, SQLite, schema, code
  layout. For anyone changing TamarackDB itself.
- `operations/`: running an instance: install (local, systemd, Docker),
  configuration (the only settings table), security, health check,
  observability, logs, dev mode, maintenance.
- `backup.md`: `tamarackdb-backup`: configuration, how a run works,
  restoring, scheduling.
- `contributing/`: building from source, tests, the demo dataset, the
  documentation site.
- `README.md`: kept short: logo, badges, a one-paragraph intro, a short
  feature list, a single link to the documentation site at
  <https://tamarackdb.github.io/> without listing its pages, a contributing
  note, and a closing "Trivia" section on the name. No config tables, no
  Docker examples, no build instructions live in the README itself.

## Page layout

- A page opens with one or two sentences on what it covers.
- Within a section: rules as a bulleted list, one rule per bullet, edge
  cases included. Keep the reason for a rule in a short "**Why.**"
  paragraph, in prose, when a rewrite might otherwise simplify it in the
  wrong place.
- Normative rules (what a client, a client library, an application, or a
  rewrite of TamarackDB has to do) use the RFC 2119 key words: MUST,
  MUST NOT, SHOULD, SHOULD NOT, MAY. No SHALL, no RECOMMENDED.
  Descriptions of how the server works stay ordinary sentences. A page
  that uses the key words says so near the top, with a link to
  `/docs/concepts/overview/#key-words`.

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
- A page that replaces another keeps the old URL working with Hugo
  `aliases` in its front matter.
- Build locally from `docs/` with `npm ci` then `npm run dev`.

## Versioning context

TamarackDB is pre-1.0. Don't write migration guides or deprecation notices
for internal changes; breaking changes to schema, config shape, or
internal APIs are expected before v1.0.
