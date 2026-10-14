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
- Set `authTokenFile`: the proxy opens the API to the network. The proxy passes the `Authorization` header through.
- In Docker, the server listens over TCP: keep its port private (see [Install](/docs/operations/install/#docker)).

## Bearer token

Setting `authTokenFile` turns authentication on. The file holds the token alone, in plain text. To create one:

```sh
sudo install -o tamarackdb -g tamarackdb -m 600 /dev/null /etc/tamarackdb/token
openssl rand -hex 32 | sudo tee /etc/tamarackdb/token > /dev/null
```

- Every route then needs `Authorization: Bearer <token>`, `/health` and `/stats` included.
- A request without the right token gets `401 Unauthorized`.
- The server removes spaces and line breaks at the start and end of the file.
- The server refuses to start when the file is missing, unreadable, or empty.
- There is one token. The server reads it at startup: changing it means restarting the server.
- Without `authTokenFile`, the default, no request is checked.
- The server refuses to start without `authTokenFile` when it listens over TCP on an address other than loopback, such
  as `0.0.0.0`. Set `allowNoAuth` to `true` to start it anyway, when something else keeps the port private. `devMode`
  implies it.
- On the unix socket, `allowNoAuth` has no effect: `socketMode` decides who can connect.

## Files

Anyone who can read the database file reads every event and projection, whatever the token and `socketMode` say.

- `tamarackdb-init` and `tamarackdb-backup` create a data directory as `0700` and a database file as `0600`.
- An existing directory or file keeps its permissions: the server never changes them. Give a directory you create
  yourself `0700`.
- The token file must be readable by the server's user only: `chmod 600`. With systemd's `LoadCredential=`, it can be
  owned by root instead (see [Install](/docs/operations/install/#systemd)).
- `devMode` stays off in production: it exposes `DELETE /events`, which deletes every event.
