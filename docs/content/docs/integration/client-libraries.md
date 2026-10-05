---
title: "Client libraries"
description: "What every client of the TamarackDB protocol must do: keep positions whole, spot a cut page, treat versions as opaque, handle transactions and lost responses."
slug: "client-libraries"
weight: 1
---

What every client of the protocol must do, whatever its language. Each rule is stated in brief, with a link to the
page that states it in full.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Positions

- A client that keeps a position MUST keep the store ID and the Sequence Position together. If a later read returns
  another store ID, the position no longer means anything: the client starts over from the beginning (see
  [Store ID](/docs/concepts/store-id/#positions)).

## Reads

- A client MUST tell the trailer apart from an event by its shape: a `hasMore` key for
  [`QUERY /events`](/docs/http-api/read-events/#response), an `end` key in a
  [transaction](/docs/http-api/transactions/#reading-events).
- A client MUST treat a response with no trailer as cut short, never as complete. Outside a transaction, it resumes
  after the last event it fully received (see [A page cut short](/docs/http-api/read-events/#a-page-cut-short)). In a
  transaction, it abandons the transaction and runs the command again.
- A client SHOULD parse a read line by line, as it arrives, and read each line within 30 seconds.

## Projections

- A client MUST treat a projection version as opaque: compare it only for equality, and never compute it (see
  [Projections](/docs/concepts/projections/#versions)). A transaction never shows one.

## Transactions

- After a read of events, the next call MUST be the write of events that closes it, with events or an empty list
  (see [Transactions](/docs/concepts/transactions/#one-decision-one-read-one-write)).
- A projection MUST be read in the transaction before it's written.
- After a `409`, or a `404 TransactionNotFound`, the client MUST run the whole command again, in a new transaction.
  Any error ends a transaction (see [Transactions](/docs/concepts/transactions/#the-end-of-a-transaction)).
- A client SHOULD abandon a transaction it no longer needs, for example in the error handler around a command.
- A client SHOULD give each event of a write the `time` the write returned, before the code that reacts to it runs:
  it's the `time` the event will carry once committed.

## Lost responses

- A commit whose response is lost can't be sent again. The application runs the command again, in a new transaction.
  An event written by a decision whose read doesn't match it, and not in reaction to one whose read does, would be
  written twice: the command MUST find out by other means (see
  [A lost response](/docs/http-api/transactions/#a-lost-response)).
- A `POST /projections` whose response is lost MAY be sent again as is: it's never applied twice. A `409` on the retry
  doesn't say which attempt won, so the client reads the projections again (see
  [A lost response](/docs/http-api/projections/#a-lost-response)).

## Testing

- [`POST /reset`](/docs/http-api/reset/), in development mode, empties the store between tests.
- [`testdata/query-cases.json`](https://github.com/tamarackdb/tamarackdb/blob/main/testdata/query-cases.json) holds
  queries, events, and whether each query selects each event, in the shapes of the HTTP API.
