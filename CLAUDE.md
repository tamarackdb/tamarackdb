# Documentation writing guidelines

These rules apply to all documentation in this repo: `README.md`, every
`docs/*.md` file (including the technical `docs/design.md`), and prose in
Go doc comments.

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

Docs are split by audience, not by topic:

- `docs/design.md`: internal design, code architecture, logic rules,
  technical detail. Audience: anyone changing TamarackDB itself.
- `docs/integration.md`: reading/appending events over HTTP, optimistic
  concurrency, error responses. Audience: people writing a client library
  or application integration against TamarackDB's HTTP API. Excludes
  anything operational.
- `docs/deploy.md`: configuration, running the binary, Docker,
  provisioning/migration tool usage, health check, observability
  (`/metrics`, `/debug`), logs. Audience: whoever runs an instance.
- `docs/build.md`: compiling, testing, Makefile targets, the demo/seed
  tool (`cmd/demo`). Audience: contributors building TamarackDB from
  source.
- `docs/backup.md`: `tamarackdb-backup` usage, how it works, scheduling
  with cron/systemd. Audience: whoever needs a standing backup copy of an
  instance's events.
- `README.md`: kept to the strict minimum: logo, one-paragraph intro, and
  a links section pointing to the docs above. No config tables, no Docker
  examples, no build instructions live in the README itself.

When adding new documentation content, place it by asking "who reads this
to do their job," not by topic proximity.

## Versioning context

TamarackDB is pre-1.0. Don't write migration guides or deprecation notices
for internal changes; breaking changes to schema, config shape, or
internal APIs are expected before v1.0.
