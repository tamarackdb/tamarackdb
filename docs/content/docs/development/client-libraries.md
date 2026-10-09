---
title: "Client Libraries"
description: "The client libraries available for TamarackDB, with a link to each one's repository: the fastest way to build an application on the event store."
slug: "client-libraries"
weight: 1
---

A client library is the simplest way to build an application on TamarackDB. It handles the HTTP calls, the
transactions, and the rules of the protocol for you.

## PHP

[tamarackdb-php](https://github.com/tamarackdb/tamarackdb-php) covers the whole HTTP API: transactions, reading and
writing events, projections, rebuilds, and pauses. It's a low-level client, for an event sourcing framework or for an
application directly.

```sh
composer require tamarackdb/tamarackdb-php
```

Its README shows how to use it.

## Other languages

Any language that speaks HTTP and JSON can call TamarackDB. To write a client, read [Concepts](/docs/development/concepts/),
the [HTTP API](/docs/development/http-api/), and [Writing a Client](/docs/development/writing-a-client/).
