---
title: "Quickstart"
description: "An overview of TamarackDB with curl: start the server with Docker, read events in a transaction, decide, write a new event, and commit it."
slug: "quickstart"
weight: 5
---

An overview of how an application uses TamarackDB: start the server, read events in a transaction, write a new event,
and commit. Every step uses `curl`. The responses assume a store that already holds a few events.

## Start the server

```sh
docker run -d --rm --name tamarackdb -p 127.0.0.1:8085:8085 \
  ghcr.io/tamarackdb/tamarackdb:latest
```

The API is at `http://127.0.0.1:8085`. `docker stop tamarackdb` stops the server and deletes its data. To keep the
data, or to run the server another way, see [Install](/docs/operations/install/).

## Read events

A command reads first, decides, then writes. Here, it subscribes `student-345` to `course-123`. Begin a transaction:

```sh
curl -X POST http://127.0.0.1:8085/tx
```

```json
{"txId":"d33c4f07-e8e4-494a-833c-ffebc99ca008"}
```

In the next calls, replace `<txId>` with that value. Read the definition of `course-123`, and the registration and
subscriptions of `student-345`:

```sh
curl -X QUERY http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{
    "query": [
      {
        "types": ["course-defined"],
        "identifiers": [
          {"name": "courseId", "value": "course-123"}
        ]
      },
      {
        "types": ["student-registered", "student-subscribed"],
        "identifiers": [
          {"name": "studentId", "value": "student-345"}
        ]
      }
    ]
  }'
```

```
{"sequence":1,"time":"2026-10-01T09:00:00.000000Z","type":"course-defined","identifiers":{"courseId":"course-123"},"metadata":{},"payload":"..."}
{"sequence":2,"time":"2026-10-01T09:05:00.000000Z","type":"student-registered","identifiers":{"studentId":"student-345"},"metadata":{},"payload":"..."}
{"sequence":7,"time":"2026-10-02T14:30:00.000000Z","type":"student-subscribed","identifiers":{"courseId":"course-234","studentId":"student-345"},"metadata":{},"payload":"..."}
{"end":true}
```

- Each line is one event that matches the query. `sequence` is its place in the log.
- `identifiers` are the event's tags. A query selects events by their tags and their type.
- `payload` is a string. The server stores it and never interprets it.
- The last line marks the end of the read.

## Write an event

`course-123` exists, and `student-345` is registered and subscribed to `course-234` only. Write the decision:

```sh
curl -X POST http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      {
        "type": "student-subscribed",
        "identifiers": {"courseId": "course-123", "studentId": "student-345"},
        "payload": "..."
      }
    ]
  }'
```

```json
{"time":"2026-10-09T02:13:14.923311Z"}
```

The event waits in the transaction until the commit:

```sh
curl -i -X POST http://127.0.0.1:8085/tx/<txId>/commit
```

```
HTTP/1.1 204 No Content
```

The event is written. If another write had appended an event matching the query since the read, the commit would get
`409 Conflict`, and the command would run again.

## Next

- [Client Libraries](/docs/development/client-libraries/): build an application with a client.
- [Concepts](/docs/development/concepts/): events, the Append Condition, transactions, and projections.
- [Install](/docs/operations/install/): run an instance in production.
