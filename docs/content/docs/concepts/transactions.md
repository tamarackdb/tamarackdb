---
title: "Transactions"
description: "How a transaction works: it lives in the server's memory, pairs each read of events with a write, and writes it all at once, or nothing, at commit."
slug: "transactions"
weight: 3
---

A transaction groups the decisions of one command, with the events and projections they cause, and writes them all at
once at commit. It lives in the server's memory, and locks nothing while it lives.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## The life of a transaction

1. **Begin.** The server returns a transaction ID, and keeps the current [store ID](/docs/concepts/store-id/) with
   it.
2. **Decisions.** Each decision reads events, then writes its events, or none.
3. **Projections.** Between two decisions, the application reads and writes projections.
4. **Commit.** The server checks every decision and every projection, then writes everything at once, or nothing.

The calls are in [HTTP API: Transactions](/docs/http-api/transactions/).

## One decision, one read, one write

- A read of events in a transaction opens a **condition**: the query, and the Sequence Position the read reached.
- The next call on the transaction MUST be the write of events that closes it. It carries the events the decision
  produced, or an empty list.
- A write of events with no open condition is refused. A decision that rests on no event reads with `"none"` first
  (see [Query grammar](/docs/http-api/query-grammar/)).
- A second read while a condition is open is refused. A decision that needs two queries combines them, as items of one
  query.
- An empty write closes the condition too. It's the decision to do nothing, and it's checked at commit like any other.
- Every read in a transaction becomes a condition. A read that doesn't serve a decision goes through
  [`QUERY /events`](/docs/http-api/read-events/), outside the transaction.

**Why.** Nothing can come between a read and its write. So no event of the transaction can be added after a read and
before the decision built on it. An event the transaction adds later, that matches a condition already closed, is not a
conflict: each decision has its place in the order of the transaction's events, and what comes after it lands after it
in the log.

**Why the empty write is checked.** A customer has 950 points, and is promoted at 1,000. Two transactions add points at
the same time: one adds 30, the other 40. Each one reads the customer's points, sees 980 or 990, and decides not to
promote. Nothing else ties the two transactions. If a decision to do nothing weren't checked, both would commit: the
customer would have 1,020 points and never be promoted. Since it is, the second commit is refused. Its command runs
again, sees 1,020 points, and promotes the customer.

## What a read sees

- The committed events that match, in Sequence Position order, then the transaction's own pending events that match,
  in the order they were written.
- A pending event has its `time` and no `sequence`: it gets its Sequence Position at commit (see
  [Events](/docs/concepts/events/#time)).
- Every event that matches, with no page limit: a decision must see all of them.
- What is committed when the read runs. Two reads of one transaction can see different states, since another write
  may commit between them. Each read's own condition protects the decision built on it.

## Projections in a transaction

- Projections are read and written only while no condition is open. A decision rests on events, never on a
  projection.
- A projection MUST be read in the transaction before it's written. The server then knows its version, or that it
  doesn't exist.
- A write of projections upserts or deletes. Deleting a projection that doesn't exist does nothing. One write names a
  projection at most once.
- A later read returns the projection as the transaction left it.
- At commit, the server turns what the transaction did to each projection into one create, replace, or delete, at the
  version read. A projection the transaction only read is neither written nor checked.

What a projection may use is in [Projections](/docs/concepts/projections/#what-a-projection-may-use).

## The commit

- The commit waits for its turn, behind the writes that arrived before it, like any write.
- In its turn, the server checks the store ID, then every condition, then every projection. Then the events get their
  Sequence Positions, and everything is written. All of it, or none of it.
- A condition fails if an event that matches its query was committed after the position its read reached. The `409`
  names it by its rank among the transaction's reads: `conditions[0]` is the first.
- A projection fails if it no longer has the version read, or if it was created since it was read as missing.
- A transaction with nothing to write commits at once, without waiting for a turn, and without checking its
  conditions: a condition only protects what is written.
- The transaction is over after its commit, whatever the outcome. After a `409`, the application MUST run the whole
  command again, in a new transaction.
- A commit whose response is lost can't be sent again: the transaction no longer exists. The application checks
  whether its write happened, for example by reading a projection the transaction wrote.

## The end of a transaction

A transaction ends:

- at its commit;
- when the application abandons it;
- at its first error, whatever the call: a broken rule, an invalid body, a conflict;
- after [`txIdleTimeout`](/docs/operations/configuration/) without a call;
- when the server stops.

Then every call on it gets `404 TransactionNotFound`. The server keeps nothing of a transaction that ended, so it can't
tell these cases apart.

- An application SHOULD abandon a transaction it no longer needs, for example in the error handler around a command.
  Otherwise the transaction expires.
- A transaction has no limit on how long it lives, as long as calls keep coming. A slow transaction only harms itself:
  the longer it lives, the more likely its commit is refused.

**Why a stop loses transactions.** A stop is rare, and costs little: the application runs its command again, as after a
`409`. Nothing has to be saved, or rebuilt at startup.

## Many transactions at once

- Many transactions live at the same time. None of them holds anything on the server: reads never wait, and a slow
  transaction blocks no one.
- Calls on one transaction take turns: a second call waits for the first to end.
- Conflicts between transactions are found at commit, one commit at a time. A transaction that became stale learns it
  then, with `409 ConcurrencyException`. No one is warned earlier.

## Outside a transaction

[`POST /write`](/docs/http-api/write/) writes events and projections in one request, with no transaction. It serves
what isn't a command: a projector that catches up on its own, a rebuild, a script. Its Append Conditions and projection
versions are sent by the client (see [Append Condition](/docs/concepts/append-condition/)).

## The guarantee

The guarantee is exactly the one of the DCB specification (see
[DCB compliance](/docs/concepts/append-condition/#dcb-compliance)). Only what a condition or a projection version
expresses is protected. A broader guarantee is a business rule of the application.
