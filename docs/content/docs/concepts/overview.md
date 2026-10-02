---
title: "Overview"
slug: "overview"
weight: 1
aliases:
  - /docs/architecture/
---

TamarackDB is an event store in Go. It follows the [DCB (Dynamic Consistency Boundaries)
specification](https://dcb.events/specification/), is reachable over HTTP, and stores everything in one SQLite file.

## What it holds

- **Events**: an ordered, append-only log (see [Events](/docs/concepts/events/)).
- **Projections**, optionally: the current state an application computes from events (see
  [Projections](/docs/concepts/projections/)). An application that keeps its projections elsewhere never touches
  them.

Several applications can share one instance when they share events. TamarackDB doesn't track which application wrote
an event.

## Scope

- TamarackDB serves applications with modest throughput.
- It runs as a single instance ("single brain"): one process, one SQLite file, one writer, no clustering.
- It trades speed for simplicity, on purpose. A system that needs high write throughput is not a good fit.
- Eventual consistency is supported, not imposed: each application chooses (see
  [Transactions](/docs/concepts/transactions/)).

## Terms

These pieces are code in the application. TamarackDB never runs them: it stores what they read and write.

- **Command**: one request to change something, handled by the application.
- **Decision model**: reads the events a command needs, decides, and appends new events.
- **Event handler**: code that reacts to events. There are two kinds:
  - **Projector**: computes projections from events and writes them.
  - **Processor**: reads events and may append more events in response.
- **Transaction**: what one command, or one event handler run, collects before sending it to the server. It lives in
  the client library (see [Transactions](/docs/concepts/transactions/)).
- **Write**: everything one transaction sends to the server at once: events, Append Conditions, and projection
  changes (see [`POST /write`](/docs/http-api/write/)).

## Key words

Rules that a client, a client library, an application, or a rewrite of TamarackDB has to follow use the key words
MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY, as described in [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) and
[RFC 8174](https://www.rfc-editor.org/rfc/rfc8174): they mean that only when written in capitals. A page that uses
them says so at the top.

## Where to go next

- [The courtyard](/docs/concepts/courtyard/): a picture of the whole model.
- [HTTP API](/docs/http-api/conventions/): the contract between TamarackDB and its clients.
- [Client libraries](/docs/client-libraries/): what a client library must do.
- [Server internals](/docs/server-internals/write-fifo/): how the server works inside, and
  [where each part lives in the code](/docs/server-internals/code-layout/).
- [Operations](/docs/operations/install/): running an instance.
