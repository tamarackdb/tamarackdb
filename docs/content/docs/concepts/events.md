---
title: "Events"
slug: "events"
weight: 4
---

An event is a fact the application recorded. Once written, it never changes and is never removed.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Fields

| Field | Set by | What it is |
|---|---|---|
| `type` | the client | A non-empty string naming what happened, such as `user-renamed` |
| `identifiers` | the client | Business identifiers, as tags (see below) |
| `metadata` | the client | Everything else about the event, as tags (see below) |
| `payload` | the client | An opaque string |
| `sequence` | the server | The event's Sequence Position (see below) |
| `time` | the server | When the server wrote it (see below) |

## Tags: identifiers and metadata

A **tag** is a `{name, value}` pair. An event carries two separate sets of tags:

- **Identifiers** name domain concepts: `userId`, `courseId`, and so on.
- **Metadata** is everything else: who wrote the event, correlation, tenant, and so on.

Rules:

- The two sets are separate namespaces: the same name can appear in both without clashing, and a query for a
  `userId` identifier doesn't match a `userId` metadata entry.
- On the wire, each set is an object whose values are a string, or an array of strings:

  ```json
  { "courseId": ["foo", "bar"], "otherId": "baz" }
  ```

  Each key is a name. Each value, or array element, is its own tag: this event has both `courseId:foo` and
  `courseId:bar`.
- An empty array as a value gets `400`: to carry no value, leave the key out.
- An event carries at most **20** identifiers and **20** metadata entries, counted as tags. These limits are fixed,
  not configuration.
- An event never carries the same `{name, value}` pair twice in one set. A duplicate gets `400`, instead of being
  dropped silently.

**Why.** An event should stay a short, meaningful statement, not a container for a long list of values. The DCB
specification has a single set of tags; splitting it in two is a naming choice. Both sets match exactly like DCB
tags, and the application knows how to read each one back.

## Payload

- `payload` is an opaque string. The server never parses or checks it.
- Its format (JSON, XML, or anything else) is a convention of the writing application, usually based on `type`.

## Sequence Position

- Every event gets a **Sequence Position** (`sequence`) when it's written: an integer, starting at 1, increasing by
  one with each event, with no gap.
- It's the order of the log, and the only order to rely on.
- A Sequence Position only means something next to the store ID it was read with (see
  [Store ID](/docs/concepts/store-id/)).

## Time

- `time` is when the server wrote the event, read from the server's clock during the write.
- It's in RFC 3339 format, always in UTC (`Z`), with exactly 6 fractional digits:
  `2026-09-01T14:23:05.123456Z`.
- Every event of one write shares the same `time`. Order within a write comes from `sequence`.
- `time` usually follows `sequence` order, but nothing guarantees it: order events by `sequence`, never by `time`.
- The store has no time zone setting. Converting to local time is the application's job.
- No read filters on `time` (see [`QUERY /events`](/docs/http-api/read-events/)).

**Why.** Day to day, NTP corrects a small drift smoothly, without moving the clock back. A jump back is still
possible: a large gap corrected at once (often at boot), a virtual machine resumed, the time set by hand, a leap
second handled badly. A decision or a read SHOULD NOT depend on `time`. An application that looks events up by
period tags them when it writes them (a `month` metadata entry, for example) and queries that tag.

## Size

- An event is at most `maxEventSize` bytes (see
  [Configuration](/docs/operations/configuration/)).
- Its size is the UTF-8 byte length of its `type`, of every tag's name and value, and of its `payload`. A multi-byte
  character counts for more than one byte.
- A write carrying a larger event gets `413 PayloadTooLarge`.

**Why.** The limit keeps an event a short statement about the world, not a data container, and keeps a decision
model's replay fast, even over hundreds of thousands of events. Larger content (files, images) belongs in external
storage, referenced from the event.
