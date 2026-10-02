---
title: "Client libraries"
description: "What a TamarackDB client library must do: keep pending writes, match them in reads, check conditions against them, send the net effect, and retry safely."
slug: "client-libraries"
weight: 1
---

What a client library does on top of the [HTTP API](/docs/http-api/conventions/). The server has no transaction that
spans several requests: the library keeps the transaction (see [Transactions](/docs/concepts/transactions/)).

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Read first

A library builds on these pages:

1. [Transactions](/docs/concepts/transactions/), [Append Condition](/docs/concepts/append-condition/), and
   [Projections](/docs/concepts/projections/): the model the library implements.
2. [Conventions](/docs/http-api/conventions/): connecting, request bodies, the store ID header.
3. [Reading events](/docs/http-api/read-events/) and [Query grammar](/docs/http-api/query-grammar/).
4. [Writing](/docs/http-api/write/) and [Projections](/docs/http-api/projections/).
5. [Errors](/docs/http-api/errors/): every code a library has to handle.

## Pending writes

A library MUST keep a transaction's pending writes in memory, and send them all in one
[`POST /write`](/docs/http-api/write/) when the transaction ends.

- A transaction with nothing to write SHOULD NOT call the server at all.
- A read in a transaction MUST return the server's matching events, then the transaction's own pending events that
  match the query, at the end. A pending event has no `sequence` or `time` yet.
- Matching pending events takes a matcher in the library. It MUST follow the
  [query grammar](/docs/http-api/query-grammar/) exactly, and SHOULD replay the
  [shared test cases](/docs/http-api/query-grammar/#shared-test-cases) in its own test suite.

**Why the matcher matters.** The server never sees how pending events merge into reads. A matcher that misses a
pending event lets a command decide without it, with no error: a processor could reserve the same seat twice, and the
write would pass. Everything between transactions still goes through the server's SQL.

## Conditions against pending events

The server checks an Append Condition against committed events only. Inside one transaction, a decision can also go
stale because of a pending event added after the read it was based on, for example by another processor of the same
command. Only the library knows the order of reads and pending events.

- When an event is added with a condition built from a read, the library MUST check that no pending event added after
  that read matches the condition.
- If one does, the command was built on a stale view. The library MUST close the transaction and report a design
  error, distinct from a `409`.

**Why not a 409.** A `409` comes from another transaction, and retrying makes sense. Here, retrying can't fix
anything: the fix is in the application.

## A condition needs the whole read

A condition built from a read carries the read's position: the store ID, and the Sequence Position of the last event
the server returned. The library only knows it once the read is paged to the end.

- If the application stops iterating early and then asks for a condition, the library MUST read the rest first.
- If the rest is empty, the application had in fact seen everything, and the condition is right.
- If events remain, the application decided without seeing events that match its own query. The library MUST close
  the transaction, report a design error, and not build the condition.

**Why.** Taking the position after the rest would be wrong: an event written after the decision, and read in that
rest, would escape the check. Taking the position of the last event seen would fail every write, because of the older
events left unread. An application that only needs to know whether an event exists narrows its query, or uses a
condition with no read.

## Processors running at once

To check a condition against pending events, a read remembers how many pending events existed when it merged them:
its marker.

- A library that runs several processors of one command at the same time (fibers, coroutines, threads) MUST take the
  marker and the list of pending events to merge at the same moment, with no suspension between the two: right after
  the server's last page arrives, just before the merge.
- Checking a new pending event against its condition and adding it to the list MUST also happen in one block.

**Why.** Otherwise another processor can add a pending event in between, and the check no longer protects anything.
When two processors of one command depend on each other, the check then fails or passes depending on timing. That's
still a design error: the fix is in the application (run them one after the other, or split them differently), not in
a retry, which could pass by luck and hide the bug.

## Projection changes

A library MUST keep the projections touched in a transaction by `type` + `id`, with the version read from the
server, and send only the net effect:

| At the start | What the application does | Sent with the write |
|---|---|---|
| Not on the server | Saves it, once or more | `create`, with the last state |
| Not on the server | Saves it, then deletes it | Nothing |
| Read at version v | Saves it, once or more | `replace` v, with the last state |
| Read at version v | Deletes it | `delete` v |
| Read at version v | Deletes it, then saves it | `replace` v, with the last state |

- A write can't carry the same `type` + `id` twice, so a delete followed by a save is one `replace`, never a `delete`
  and a `create`.
- A read in the transaction MUST return the pending state of a projection the transaction already touched.
- Deleting a projection the transaction neither read nor created is a design error: a `delete` needs the stored
  version, and the library doesn't know it. The library MUST close the transaction and report the error right away,
  without waiting for the write. The application reads the projection first.

## Positions

A Sequence Position only means something next to the store ID it was read on (see
[Store ID](/docs/concepts/store-id/#positions)). A library MUST hand them out together.

## Retries

- After a `409`, the application reads again, decides again, and sends a new write, in a new transaction.
- After a lost response, a retry is safe only under the conditions in
  [A lost response](/docs/http-api/write/#a-lost-response). A library MAY offer the `writeId` recipe described there.

## Testing a library

- [`POST /reset`](/docs/http-api/reset/) empties the store between tests, in dev mode.
- `tamarackdb-demo` fills a data directory with a large set of made-up events and projections, to test against
  realistic volume (see [Building from source](/docs/contributing/building-from-source/#demo-dataset)).
