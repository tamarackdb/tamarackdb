---
title: "Quickstart"
description: "Run TamarackDB with Docker and try it with curl: write events in a transaction, read them back, and see a commit refused because its read is stale."
slug: "quickstart"
weight: 5
---

Run an instance on your machine, write events in a transaction, read them back, and see a commit refused. Every step
uses `curl`.

## Run the server

```sh
docker run -d --rm --name tamarackdb -p 127.0.0.1:8085:8085 \
  ghcr.io/tamarackdb/tamarackdb:latest
```

The API is at `http://127.0.0.1:8085`. `docker stop tamarackdb` stops the server and deletes its data. To run it
another way, see [Install](/docs/operations/install/).

## Write in a transaction

Course `c1` is defined, with room for one student. Begin a transaction:

```sh
curl -X POST http://127.0.0.1:8085/tx
```

```json
{"txId":"ca05c3fd-0d64-4334-86e2-f40d9b68681e"}
```

In the next calls, replace `<txId>` with that value.

A decision reads first, then writes. Read every event about course `c1`:

```sh
curl -X QUERY http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{ "query": [ { "identifiers": [ { "name": "courseId", "value": "c1" } ] } ] }'
```

```
{"end":true}
```

The course doesn't exist yet: the read returns only its last line. Write the decision:

```sh
curl -X POST http://127.0.0.1:8085/tx/<txId>/events \
  -H "Content-Type: application/json" \
  -d '{ "events": [ { "type": "course-defined", "identifiers": { "courseId": "c1" }, "payload": "{\"capacity\":1}" } ] }'
```

```json
{"time":"2026-10-04T18:33:08.516677Z"}
```

- `identifiers` are tags that name things in your domain. A query selects events by their tags and their type (see
  [Events](/docs/concepts/events/)).
- `payload` is a string. The server stores it and never reads it.
- Nothing is written yet: the event waits in the transaction.

Commit:

```sh
curl -i -X POST http://127.0.0.1:8085/tx/<txId>/commit
```

```
HTTP/1.1 204 No Content
```

The event is written, and the transaction is over.

## Read the events back

Outside a transaction, read every event:

```sh
curl -i -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{ "query": "all" }'
```

```
HTTP/1.1 200 OK
Content-Type: application/x-ndjson
X-Tamarackdb-Store: 3d6557d3-c262-4e57-b138-b7d148eb95c9

{"sequence":1,"time":"2026-10-04T18:33:08.516677Z","type":"course-defined","identifiers":{"courseId":"c1"},"metadata":{},"payload":"{\"capacity\":1}"}
{"hasMore":false}
```

- The event now has a Sequence Position: its place in the store's history. Its `time` is the one its write returned.
- The last line says whether more events are left to read (see [Reading events](/docs/http-api/read-events/)).

## A commit based on a stale read

Two students subscribe at the same time. Each one runs in its own transaction: begin two transactions, `<txA>` and
`<txB>`. In each one, read course `c1` as above. Both see the course with room for one student, and both decide to
subscribe their student:

```sh
curl -X POST http://127.0.0.1:8085/tx/<txA>/events \
  -H "Content-Type: application/json" \
  -d '{ "events": [ { "type": "student-subscribed", "identifiers": { "courseId": "c1", "studentId": "s1" }, "payload": "{}" } ] }'

curl -X POST http://127.0.0.1:8085/tx/<txB>/events \
  -H "Content-Type: application/json" \
  -d '{ "events": [ { "type": "student-subscribed", "identifiers": { "courseId": "c1", "studentId": "s2" }, "payload": "{}" } ] }'
```

Commit `<txA>`: it gets `204`. Then commit `<txB>`:

```sh
curl -i -X POST http://127.0.0.1:8085/tx/<txB>/commit
```

```
HTTP/1.1 409 Conflict
Content-Type: application/json

{"error":"ConcurrencyException","message":"conditions[0] no longer holds"}
```

Nothing of `<txB>` is written. Its decision rests on its first read, `conditions[0]`, and an event about `c1` was
committed since. The application runs the command again in a new transaction: this time the read shows the course
full, and the decision changes (see [Transactions](/docs/concepts/transactions/#the-commit)).

## Next

- [Mental model](/docs/concepts/mental-model/): a picture of the whole model.
- [Transactions](/docs/concepts/transactions/): how a command reads, decides, and writes.
- [Example](/docs/http-api/example/): one order in an online store, call by call.
- [Install](/docs/operations/install/): running an instance in production.
