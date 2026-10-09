---
title: "Writing a Client"
description: "What a client of the TamarackDB protocol has to do, whatever its language: positions, reads cut short, transactions, retries, and lost responses."
slug: "writing-a-client"
weight: 4
---

What a client of the protocol has to do, whatever its language. The calls themselves are in the
[HTTP API](/docs/development/http-api/).

## Positions

- Keep the store ID and the Sequence Position together.
- If a read returns another store ID, start over from the beginning.

## Reads

- Tell a trailer apart from an event by its shape: a `hasMore` key for `QUERY /events`, an `end` key in a transaction.
- Treat a response with no trailer as cut short. Outside a transaction, resume after the last full event. In a
  transaction, abandon it and run the command again.
- Parse a read line by line, as it arrives.

## Transactions

- After a read of events, the next call is the write of events that closes it, with events or an empty list.
- Read a projection in the transaction before you replace or delete it. A `create` needs no read.
- Send one call at a time on a transaction.
- After `409 ConcurrencyException` or `404 TransactionNotFound`, run the whole command again, in a new transaction.
- `POST /tx` can get `503 Paused`. What to do with the command is the application's choice.
- Abandon a transaction you no longer need, for example in the error handler around a command, and ignore the
  response.
- Give each event of a write the `time` the write returned: it's the `time` the event keeps once committed.

## Projections

- Treat a version as opaque: compare it for equality only.
- After `POST /projections`, use the version from the response for the next change.

## Lost responses

- A commit can't be sent again. Run the command again, in a new transaction.
- A `POST /projections` can be sent again as is. On a `409`, read the projections again.

## Testing

- In development mode, empty the store between tests with `POST /pause`, `POST /reset`, then `POST /resume`.
- [`testdata/query-cases.json`](https://github.com/tamarackdb/tamarackdb/blob/main/testdata/query-cases.json) lists
  queries, events, and whether each query selects each event.

## How the server works

How the server works inside, and the reasons behind its behavior, are in the Go comments of the
[repository](https://github.com/tamarackdb/tamarackdb). `go doc ./...` gives the role of each package.
