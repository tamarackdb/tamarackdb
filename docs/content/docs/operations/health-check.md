---
title: "Health check"
description: "GET /health: how a process supervisor or a load balancer checks that a TamarackDB instance responds and that its SQLite database can be reached."
slug: "health-check"
weight: 4
---

`GET /health` tells a process supervisor or a load balancer whether the server can serve requests.

```sh
sudo -u tamarackdb curl --unix-socket /run/tamarackdb/tamarackdb.sock http://localhost/health
```

The socket only lets in the users `socketMode` allows: the server's own user by default, hence the `sudo -u`. On a TCP
deployment:

```sh
curl http://127.0.0.1:8085/health
```

- It checks that the process responds and that SQLite is reachable, with a trivial `SELECT 1` on the read pool.
- On success: `200 OK`, with whether a [pause](/docs/http-api/pause/) is in place, and the version.

  ```json
  { "status": "ok", "paused": false, "version": "1.2.3" }
  ```

- `paused` is always there. It's `true` only once a pause is in place, not while it's requested. A paused server still
  answers `200`: it serves reads and writes of projections, so a load balancer keeps it in.

- When SQLite can't be reached: `503 Unavailable`, the usual signal for "not ready right now", rather than the `500` of
  an ordinary failure.
- When `enableAuth` is on, it needs the token like every other route (see
  [Security](/docs/operations/security/#bearer-token)).
