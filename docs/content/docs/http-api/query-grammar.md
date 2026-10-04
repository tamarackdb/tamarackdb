---
title: "Query grammar"
description: "The query grammar used to read events and in Append Conditions: \"all\", \"none\", query items, the matching rules, and the size limits."
slug: "query-grammar"
weight: 2
---

A query selects events by type and tags. The same grammar serves `query` in a read and `failIfEventsMatch` in an Append
Condition. It follows the DCB specification.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Shape

A query is one of three things:

- the string `"all"`, which matches every event;
- the string `"none"`, which matches no event;
- an array of query items:

```json
[
  {
    "types": ["user-created", "user-updated"],
    "identifiers": [ { "name": "userId", "value": "123" } ],
    "metadata": [ { "name": "tenantId", "value": "acme" } ]
  },
  { "types": ["some-other-event"] }
]
```

`"all"` and `"none"` are exact and lowercase: any other string, such as `"All"` or `"*"`, gets `400`.

**Why `"none"`.** A decision in a transaction that rests on no event still reads first, with `"none"` (see
[Transactions](/docs/concepts/transactions/#one-decision-one-read-one-write)). Its read returns nothing, and the condition
it opens can never fail.

## Matching

- The items are combined with **OR**: an event matches the query if it matches any item.
- Within one item, the three keys are combined with **AND**:
  - `types`: **OR**, the event's type is one of the listed types;
  - `identifiers`: **AND**, the event carries every listed identifier;
  - `metadata`: **AND**, the event carries every listed metadata entry.
- A key left out doesn't filter.
- Identifiers and metadata are separate: a `userId` in `metadata` doesn't match a `userId` asked for in `identifiers`.
- Values are compared exactly, byte for byte: case counts, and no Unicode normalization happens.
- An identifier with several values on the event (see [Events](/docs/concepts/events/#tags-identifiers-and-metadata))
  matches if any one of them is the value asked for.

## Rules

- Every array (the query itself, `types`, `identifiers`, `metadata`) MUST be non-empty when present. An empty array gets
  `400`, instead of meaning something special. To leave an axis open, leave the key out. To match every event, use
  `"all"`.
- `query` is always required. A missing `query` gets `400`, instead of meaning `"all"` or `"none"`: a key lost on the
  client must never widen a read or remove the protection of a decision.
- An item MUST name at least one of `types`, `identifiers`, or `metadata`. An empty item (`{}`) gets `400`: it poses no
  constraint.
- There is no negation ("not X"). A negation describes an unlimited set of events, so nothing could guarantee that no
  future event breaks a condition built on it.
- A query carries at most **100** items, and an item at most **100** values across `types`, `identifiers`, and
  `metadata` combined. A larger query gets `400`. These limits are fixed, not configuration, and they're counted after
  duplicate items are dropped.
- Two items that are exact duplicates (same types, identifiers, and metadata, in any order) are kept once, silently. A
  client that merges several queries into one doesn't have to deduplicate them first.

**Why the size limits.** They keep the SQL a query turns into well inside SQLite's limits on expression depth and bound
parameters. A query of about 1,000 terms would otherwise fail inside SQLite, instead of getting a clear `400`.

