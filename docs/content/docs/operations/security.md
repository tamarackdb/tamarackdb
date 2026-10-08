---
title: "Security"
description: "Who can reach a TamarackDB instance and who can read its data: the unix socket, a reverse proxy for other hosts, the Bearer token, and file permissions."
slug: "security"
weight: 3
---

Who can reach an instance, and who can read its data.

Key words in capitals follow [RFC 2119](/docs/concepts/overview/#key-words).

## Unix socket

The recommended setup: TamarackDB runs on the same host as the application, on its unix socket (the default, see
[Configuration](/docs/operations/configuration/#listening)).

- Nothing goes over the network, and the socket's permissions decide who may connect.
- A transaction often makes several reads before its one write, and the socket keeps each of those round trips short.
- Connecting takes write permission on the socket. With the default `socketMode` of `"0600"`, only the server's own user
  can connect.
- If the application runs as another user, set `socketMode = "0660"` and add the application's user to the server's
  group. For a server running as `tamarackdb`, and an application running as `www-data`:

  ```sh
  sudo usermod -aG tamarackdb www-data
  ```

  The application picks up its new group once it restarts.
- The server creates the socket under a umask of `0177`, so it starts out as `0600` whatever the process's umask, then
  sets it to `socketMode`.

**Why the umask.** Under a looser umask, such as the common `002`, the socket would start out open to the group, and a
connection could slip in before `socketMode` is applied.

## Another host

The server speaks plain HTTP only, on the socket and over TCP alike. A client on another host, application or backup,
MUST go through a reverse proxy that handles TLS.

- Without TLS, bodies and the `authToken` itself travel in clear text over a network outside your control.
- Put the proxy in front of the socket. Caddy, for example, gets and renews its certificates by itself:

  ```
  tamarackdb.example.com {
      reverse_proxy unix//run/tamarackdb/tamarackdb.sock
  }
  ```

  With nginx:

  ```nginx
  server {
      listen 443 ssl;
      server_name tamarackdb.example.com;
      ssl_certificate     /etc/ssl/tamarackdb.example.com/fullchain.pem;
      ssl_certificate_key /etc/ssl/tamarackdb.example.com/privkey.pem;
      client_max_body_size 8m;

      location / {
          proxy_pass http://unix:/run/tamarackdb/tamarackdb.sock:;
      }
  }
  ```

- nginx refuses a body larger than 1 MiB by default. Set `client_max_body_size` to the server's
  `maxRequestBodySize` (see [Configuration](/docs/operations/configuration/#settings)).

- The proxy's user MUST be allowed by `socketMode`, like the application's.
- Turn `enableAuth` on: once the proxy is up, the API is reachable over the network, and the token is what keeps others
  out. The proxy passes the `Authorization` header through unchanged.
- In Docker, the server listens over TCP: how to keep its port private is in
  [Install](/docs/operations/install/#docker).

**Why a proxy, not TLS in the server.** A server that loads its certificate once at startup would need a restart,
cutting off any write in progress, each time a short-lived certificate is renewed; a proxy renews on its own. It also
handles what surrounds TLS (protocol versions, client certificates, address allowlists) better than a store should, and
keeps one path for every access from another host.

## Bearer token

- When `enableAuth` is on, every registered route needs `Authorization: Bearer <token>`: every endpoint of the HTTP API,
  `/health`, `/stats`, and, in development mode, `POST /reset` and the profiling endpoints.
- A request with no valid token gets `401 Unauthorized` before it reaches any handler.
- The token is one fixed value, `authToken`. Rotating it means changing the configuration and restarting the server:
  there's no window where two tokens both work.
- The server MUST compare the token in constant time, never with a plain string comparison: one that stops at the first
  differing byte would let an attacker guess the token one byte at a time, by timing the responses.
- `enableAuth` is off by default, for the recommended setup, where the socket's permissions already decide who may
  connect. When it's off, no request is checked at all.

**Why one token.** An instance has exactly one trusted caller: the application that owns it. If that application serves
many tenants, keeping them apart is its own job, done with tenant metadata on events.

## Files

The database file holds every event and projection in plain SQLite. Anyone who can read it bypasses `enableAuth` and
`socketMode` entirely.

- `tamarackdb-init` and `tamarackdb-backup` create a missing data directory as `0700`, and a new database file as
  `0600`, whatever the umask. SQLite gives its WAL and shared-memory files the database file's permissions.
- A directory or database file that already exists keeps its permissions: the server never changes them. Give a
  directory you create yourself `0700`.
- Which user runs which command is in [Install](/docs/operations/install/#run).
- A `config.toml` that holds `authToken` MUST be readable by the server's user only:

  ```sh
  chmod 600 /path/to/config.toml
  ```

## Development mode

`devMode` exposes `POST /reset`, which deletes every event. It MUST stay off on a production instance (see [Development
mode](/docs/operations/dev-mode/)).
