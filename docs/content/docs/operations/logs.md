---
title: "Logs"
description: "The access log of tamarackdb-server: one line per request, what it carries and never carries, logLevel, and the fixed level of each outcome."
slug: "logs"
weight: 6
---

The server logs one line per request to stdout.

## The line

```
tamarackdb-server: [WARNING] POST /write 503 27B 0.07ms
```

- It carries the level, the method, the path, the status code, the response size, and the time taken.
- It's written once the handler ends. It wraps the whole routed handler, authentication included, so a request
  turned away with `401` is logged like any other.
- It never carries a request or response body: queries, conditions, and events never reach the log.
- `logLevel` sets the lowest level written: `debug`, `info`, `warning`, or `error`. The default, `warning`, prints
  only `warning` and `error` lines. Set it to `debug` to see every request while testing an integration.

## Levels

Each outcome has a fixed level, not derived from the status code alone:

| Outcome | Status | Level |
|---|---|---|
| Successful request | 2XX | `debug` |
| Projection not found | 404 | `debug` |
| Transaction not found | 404 | `info` |
| Concurrency conflict | 409 | `debug` |
| Invalid request | 400 | `info` |
| Payload too large | 413 | `info` |
| Missing or invalid bearer token | 401 | `info` |
| Server shutting down | 503 | `info` |
| Write queue full | 503 | `warning` |
| Internal error | 500 | `error` |
| Storage unreachable | 503 | `error` |

- `debug`: the server did exactly what it should, a success or an expected rejection (a conflict, a projection that
  doesn't exist).
- `info`: not the server's fault, but worth knowing: a malformed or oversized request, a bad token, a call on a
  transaction that no longer exists, or a request turned away during shutdown.
- `warning`: a real signal of capacity or contention, such as a full write queue.
- `error`: a real failure.

The startup banner is described in [Install](/docs/operations/install/#startup-banner).
