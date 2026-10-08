---
title: "Import"
description: "Bringing an application's history over from another event store: tamarackdb-init --import, the rules it checks, the dump format, and what to do after."
slug: "import"
weight: 8
---

An application that moves to TamarackDB from another event store brings its history with it once, before it goes
live. `tamarackdb-init --import` reads a dump of that history and makes a new database from it, ready for the server.
Each event keeps its `sequence` and its `time`.

To copy a TamarackDB instance, copy its database file instead: the copy keeps the store ID.

## Running an import

Run it as the server's user (see [Install](/docs/operations/install/#run)):

```sh
sudo -u tamarackdb ./bin/tamarackdb-init --data-dir /path/to/data --import events.ndjson
```

- `tamarackdb-init` does what it does without `--import`: it creates `dataDir` if it's missing, and a database with the
  schema and a new store ID. It refuses to overwrite an existing database. Then it writes the events.
- It reads the dump in the order of its lines.
- A line that isn't exactly an event (see [The dump](#the-dump)) stops the import, with its line number.
- A dump with no event is refused.
- Each event keeps its `sequence`. The first one can be 1 or more. Each next one is the one before plus 1, or the import
  stops.
- Each event keeps the `time` of the dump, in whatever order those times come.
- `maxEventSize` doesn't apply to an import.
- The import is all or nothing: when it stops, there is no database, and a `dataDir` it created is removed. It writes
  into a file named `tamarackdb.sqlite.import-*` in `dataDir`, and gives it its final name at the end. Only a process
  killed during the import leaves that file behind: delete it.
- It writes no projection.

At the end, it prints the number of events imported, and the first and last `sequence`:

```text
tamarackdb-init: created /path/to/data/tamarackdb.sqlite, imported 1000000 event(s), sequence 1 to 1000000
```

Compare them with the source event store before you start the server. Then start it, and rebuild every projection (see
[Rebuilds](/docs/concepts/projections/#rebuilds)).

**Why a line that isn't exact stops the import.** Skipping it would lose an event without anyone knowing. An extra
field often means a misnamed field, or data from the source that would be lost.

**Why the count and the first and last sequence.** A gap in the middle of the dump stops the import. A dump missing its
first or last events has no gap: only a comparison with the source shows it.

**Why a new database, never an addition to an existing one.** The Sequence Positions and the store ID stay
consistent: no projector has a position to fix, and no client has read part of the history.

**Why keep the dump's sequences.** An application moving from another event store keeps its positions. Its projectors
resume where they were, without noticing the move.

**Why the times can come in any order.** Order comes from `sequence`, never from `time` (see
[Events](/docs/concepts/events/#time)). A `time` going back from one event to the next breaks no rule.

## The dump

One event per line, in NDJSON, in the shape of an event in a [`QUERY /events`](/docs/http-api/read-events/#response)
response. A line holds exactly these six fields, each once, no more and no less:

- `sequence`: an integer, 1 or more.
- `time`: exactly the form `2024-03-01T09:12:44.000000Z`, in UTC, with `Z`, and six digits after the second.
- `type`: a non-empty string.
- `identifiers` and `metadata`: objects, `{}` when empty, never `null`. Each follows the rules of
  [Events](/docs/concepts/events/#tags-identifiers-and-metadata), at most 20 of each included.
- `payload`: a string, `""` when there is no payload, never `null`.

A key that appears twice in one object, at any depth, is refused. Whitespace between JSON tokens is accepted. The last
line may end with a newline or not. An empty line anywhere else is refused.

```json
{"sequence":1,"time":"2024-03-01T09:12:44.000000Z","type":"order-placed","identifiers":{"orderId":"o-1"},"metadata":{"tenantId":"acme"},"payload":"..."}
```

**Why this exact format.** It's the one the server writes for every event, in the database and on the wire. A `time`
imported in another form, with an offset or three digits after the second, would give two forms of `time` in one
database.
