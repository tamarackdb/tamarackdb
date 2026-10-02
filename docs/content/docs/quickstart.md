---
title: "Quickstart"
description: "Run TamarackDB with Docker and try it with curl: write events, read them back, and see an Append Condition refuse a write based on a stale read."
slug: "quickstart"
weight: 15
---

Run an instance on your machine, write a few events, read them back, and see an Append Condition refuse a write. Every
step uses `curl`.

## Run the server

```sh
docker run -d --rm --name tamarackdb -p 127.0.0.1:8085:8085 \
  ghcr.io/tamarackdb/tamarackdb:latest
```

The API is at `http://127.0.0.1:8085`. `docker stop tamarackdb` stops the server and deletes its data. To run it
another way, see [Install](/docs/operations/install/).

## Write events

Course `c1` is defined, and student `s1` subscribes to it:

```sh
curl -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      { "type": "course-defined", "identifiers": { "courseId": "c1" }, "payload": "{\"capacity\":2}" },
      { "type": "student-subscribed", "identifiers": { "courseId": "c1", "studentId": "s1" }, "payload": "{}" }
    ]
  }'
```

```json
{"events":[{"sequence":1,"time":"2026-10-02T11:57:22.922790Z"},{"sequence":2,"time":"2026-10-02T11:57:22.922790Z"}],"projections":{"create":[],"replace":[]}}
```

- `identifiers` are tags that name things in your domain. A query selects events by their tags and their type (see
  [Events](/docs/concepts/events/)).
- `payload` is a string. The server stores it and never reads it.
- Each event gets a Sequence Position: its place in the store's history.

## Read them back

Every event about course `c1`:

```sh
curl -i -X QUERY http://127.0.0.1:8085/events \
  -H "Content-Type: application/json" \
  -d '{ "query": [ { "identifiers": [ { "name": "courseId", "value": "c1" } ] } ] }'
```

```
HTTP/1.1 200 OK
Content-Type: application/x-ndjson
X-Tamarackdb-Store: 0a719e54-4add-468e-aec0-3522f88ccf87

{"sequence":1,"time":"2026-10-02T11:57:22.922790Z","type":"course-defined","identifiers":{"courseId":"c1"},"metadata":{},"payload":"{\"capacity\":2}"}
{"sequence":2,"time":"2026-10-02T11:57:22.922790Z","type":"student-subscribed","identifiers":{"courseId":"c1","studentId":"s1"},"metadata":{},"payload":"{}"}
{"hasMore":false}
```

- The `X-Tamarackdb-Store` header holds the store ID. The next step needs it.
- Each line is one event, in Sequence Position order. The last line says whether more events are left to read (see
  [Reading events](/docs/http-api/read-events/)).

## Write based on a decision

The application has read the course up to Sequence Position 2, and decides that student `s2` can subscribe. It sends
the new event with an Append Condition: fail if an event about course `c1` was written after position 2.

Replace `<store-id>` with the value of `X-Tamarackdb-Store`:

```sh
curl -i -X POST http://127.0.0.1:8085/write \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      { "type": "student-subscribed", "identifiers": { "courseId": "c1", "studentId": "s2" }, "payload": "{}" }
    ],
    "conditions": [
      {
        "failIfEventsMatch": [ { "identifiers": [ { "name": "courseId", "value": "c1" } ] } ],
        "afterSequence": 2,
        "store": "<store-id>"
      }
    ]
  }'
```

No event about `c1` came after position 2, so the write goes through and the event gets Sequence Position 3.

## A write based on a stale read

Send the same request again. It plays a second instance of the application that read the same events at the same
time, and made the same decision:

```
HTTP/1.1 409 Conflict
Content-Type: application/json

{"error":"ConcurrencyException","message":"conditions[0] no longer holds"}
```

Nothing is written. Event 3 is about `c1` and came after position 2, so the decision rests on a read that is no longer
true. The application reads again, decides again, and sends a new write (see
[Append Condition](/docs/concepts/append-condition/#the-flow)).

## Next

- [Mental model](/docs/concepts/mental-model/): a picture of the whole model.
- [Append Condition](/docs/concepts/append-condition/): what a condition protects, and what it doesn't.
- [Conventions](/docs/http-api/conventions/): the HTTP API, endpoint by endpoint.
- [Client libraries](/docs/integration/client-libraries/): what a library does for the application.
- [Install](/docs/operations/install/): running an instance in production.
