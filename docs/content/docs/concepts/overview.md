---
title: "Overview"
description: "What TamarackDB is: a DCB event store in Go, over HTTP, on one SQLite file. What it holds, its scope, the terms used everywhere, and the RFC 2119 key words."
slug: "overview"
weight: 1
---

TamarackDB is an event store in Go. It follows the [DCB (Dynamic Consistency Boundaries)
specification](https://dcb.events/specification/), is reachable over HTTP, and stores everything in one SQLite file.

## What it holds

- **Events**: an ordered, append-only log (see [Events](/docs/concepts/events/)).
- **Projections**, optionally: the current state an application computes from events (see
  [Projections](/docs/concepts/projections/)). An application that keeps its projections elsewhere never touches
  them.

## Scope

- TamarackDB serves applications with modest throughput.
- It runs as a single instance ("single brain"): one process, one SQLite file, one writer, no clustering.
- It trades speed for simplicity, on purpose. A system that needs high write throughput is not a good fit.

## Terms

- **Transaction**: what one command reads and writes, kept in the server's memory until its commit (see
  [Transactions](/docs/concepts/transactions/)).
- **Decision**: one read of events in a transaction, then the write of the events it produced, or none.
- **Commit**: the end of a transaction, where everything it holds is checked, then written at once, or not at all.
- **Write**: what the server writes at once, in one SQLite transaction: the commit of a transaction, or one
  [`POST /projections`](/docs/http-api/projections/#writing-projections).

## Key words

Rules that a client, a client library, or an application has to follow use the key words
MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY, as described in [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) and
[RFC 8174](https://www.rfc-editor.org/rfc/rfc8174): they mean that only when written in capitals. A page that uses
them says so at the top.

## Where to go next

- [Mental model](/docs/concepts/mental-model/): a picture of the whole model.
- [Transactions](/docs/concepts/transactions/): how a command reads, decides, and writes.
- [HTTP API](/docs/http-api/conventions/): the contract between TamarackDB and its clients.
- [Client libraries](/docs/integration/client-libraries/): what every client of the protocol must do.
- [Operations](/docs/operations/install/): running an instance.
