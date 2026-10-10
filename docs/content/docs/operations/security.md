---
title: "Security"
description: "Who can reach a TamarackDB instance and who can read its data: the unix socket, a reverse proxy for other hosts, the Bearer token, and file permissions."
slug: "security"
weight: 3
---

Who can reach an instance, and who can read its data.

## Unix socket

The recommended setup: TamarackDB runs on the application's host, on its unix socket, the default.

- Nothing goes over the network. The socket's permissions decide who may connect.
- With the default `socketMode` of `"0600"`, only the server's own user can connect.
- If the application runs as another user, set `socketMode = "0660"` and add that user to the server's group. For an
  application running as `www-data`:

  ```sh
  sudo usermod -aG tamarackdb www-data
  ```

  The application picks up its new group once it restarts.

## Another host

The server speaks plain HTTP only. A client on another host, application or backup, goes through a reverse proxy that
handles TLS, in front of the socket.

With Caddy, which gets and renews its certificates by itself:

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

- Set nginx's `client_max_body_size` to the server's `maxRequestBodySize`. nginx refuses bodies over 1 MiB by default.
- The proxy's user must be allowed by `socketMode`, like the application's.
- Turn `enableAuth` on: the proxy opens the API to the network. The proxy passes the `Authorization` header through.
- In Docker, the server listens over TCP: keep its port private (see [Install](/docs/operations/install/#docker)).

## Bearer token

- With `enableAuth` on, every route needs `Authorization: Bearer <token>`, `/health` and `/stats` included.
- A request without the right token gets `401 Unauthorized`.
- There is one token, `authToken`. Changing it means changing the configuration and restarting the server.
- With `enableAuth` off, the default, no request is checked.

## Files

Anyone who can read the database file reads every event and projection, whatever `enableAuth` and `socketMode` say.

- `tamarackdb-init` and `tamarackdb-backup` create a data directory as `0700` and a database file as `0600`.
- An existing directory or file keeps its permissions: the server never changes them. Give a directory you create
  yourself `0700`.
- A `config.toml` that holds `authToken` must be readable by the server's user only: `chmod 600 config.toml`.
- `devMode` stays off in production: it exposes `DELETE /events`, which deletes every event.
