---
title: "Schema"
description: "The SQLite schema of a TamarackDB database file, why each table and index is shaped the way it is, and how the schema version is checked at startup."
slug: "schema"
weight: 6
---

The SQLite schema, and why each table and index is shaped the way it is.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

```sql
PRAGMA user_version = 1;

CREATE TABLE events (
    sequence    INTEGER PRIMARY KEY,
    time        TEXT NOT NULL,
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    identifiers TEXT NOT NULL,
    metadata    TEXT NOT NULL
);

CREATE INDEX idx_events_type ON events(type);

CREATE TABLE identifiers (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_identifiers_name_value ON identifiers(name, value, event_sequence);

CREATE TABLE metadata (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_metadata_name_value ON metadata(name, value, event_sequence);

CREATE TABLE projections (
    type    TEXT NOT NULL,
    id      TEXT NOT NULL,
    version TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (type, id)
) WITHOUT ROWID;

CREATE TABLE store (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    id        TEXT NOT NULL
);
```

## Tables

- **`events`**: one row per event. `sequence` is a plain `INTEGER PRIMARY KEY`, set by the server (see
  [Sequence Position counter](/docs/server-internals/sequence-counter/)).
- **`identifiers`** and **`metadata`**: one row per tag, keyed by `(event_sequence, name, value)`. They're
  `WITHOUT ROWID`: pure link rows, where a separate rowid would only be an extra btree. A tag is stored as a
  structured pair, not as a delimited string like `"courseId:123"`: no escaping problem, and a direct index on
  `name` + `value`.
- **`projections`**: one row per projection, keyed by `(type, id)`, `WITHOUT ROWID`. A projection has no history, so
  its natural key is its only key. The same key serves `DELETE /projections/{type}`, as a prefix.
- **`store`**: a single row holding the store ID (see [Store ID](/docs/concepts/store-id/)). The `CHECK` on
  `singleton` keeps a second row out. The row is written with the rest of the schema, in the same transaction, and
  changed only by a reset.

## Choices

- **Tag indexes.** The index `(name, value, event_sequence)` on `identifiers` and `metadata` serves query matching
  directly. `event_sequence` is in it so the index alone answers the scan.
- **Tags stored twice.** `events.identifiers` and `events.metadata` hold the tags again, in the compact object shape
  the HTTP API returns. A read hands these columns to the client exactly as stored, without decoding them or joining
  the tag tables. The tag tables serve matching and filtering, keyed by `name` and `value`.
- **`time` as text.** `time` is stored as `TEXT` in a fixed-width UTC format, which sorts the same way alphabetically
  as chronologically, and a read passes it straight through. The server MUST always write `time` in exactly that
  format (see [Events](/docs/concepts/events/#time)): an offset, or a different number of fractional digits, would
  break the ordering (`05.123Z` sorts after `05.123456Z`, since `Z` comes after every digit). No index covers `time`:
  nothing filters or orders on it.
- **Foreign keys.** The `events(sequence)` foreign keys are enforced with `PRAGMA foreign_keys = ON` on every
  connection: SQLite reads foreign key declarations but doesn't enforce them by default. It catches implementation
  bugs, such as a tag row written for an event that doesn't exist, rather than serving a functional need.

## Schema version

- The schema version is `PRAGMA user_version`, built into the binary.
- A new database file is created with the current schema and version.
- An existing file whose version doesn't match (older, or newer from a downgraded binary) is fatal at startup: the
  server logs it and refuses to start.
- The server never changes its own schema.
