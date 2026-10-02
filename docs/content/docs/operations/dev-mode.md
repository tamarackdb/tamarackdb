---
title: "Dev mode"
description: "What devMode turns on, POST /reset and Go's profiling endpoints, why it must stay off in production, and how to profile a request with pprof."
slug: "dev-mode"
weight: 9
---

`devMode` turns on two things, for a local instance or a controlled troubleshooting session. Never turn it on in
production.

| What | Purpose |
|---|---|
| [`POST /reset`](/docs/http-api/reset/) | Deletes every event and projection, and draws a new store ID |
| `/debug/pprof/*` | Go's standard profiling endpoints (CPU, heap, goroutine, and so on) |

- With `devMode` off, neither exists: a request to either gets the plain `404` of any unknown path.
- When `enableAuth` is on, both need the token like every other route.
- Turn `devMode` on only for as long as you need it, then turn it back off.

**Why profiling is dev-mode-only.** The profiling endpoints only read the running process, not the database, and run
outside the write FIFO. But a CPU or heap profile can reveal data flowing through a live request.

## Profiling a request

With `devMode` on, start a CPU profile, then send the request to look at from another terminal while it collects
samples:

```sh
go tool pprof -http=:0 "http://127.0.0.1:8085/debug/pprof/profile?seconds=30"
```

- The result opens as a flame graph once the 30 seconds are up, or once the request ends if it takes longer (raise
  `seconds` then).
- For a request that allocates heavily, such as a large `QUERY /events` page, also check `/debug/pprof/allocs`.
- For a timeline instead of an aggregate, use `/debug/pprof/trace?seconds=30` with `go tool trace`.
