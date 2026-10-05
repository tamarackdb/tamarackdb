---
title: "Example: an online store"
description: "One order in an online store as the HTTP calls an application sends: five decisions and their projections in one transaction, then an async projector."
slug: "example"
weight: 9
---

One order in an online store, as the HTTP calls the application sends. The order, its loyalty points, and its stock
reservation are written together, or not at all: an order must never exist without the stock reserved for it.

## The application

The application splits its rules into small pieces of code, one per rule. Each name below labels the code that sends a
call. TamarackDB knows none of them: it only sees the calls.

| Code | Reacts to | Reads | Writes |
|---|---|---|---|
| PlaceOrderModel | the order command | the customer | `order-placed` |
| EarnPointsModel, run by LoyaltyProcessor | `order-placed` | `"none"`: one point per dollar | `points-earned` |
| CustomerPointsProjector | `points-earned` | | the `customer-points` projection |
| PromoteCustomerModel, run by TierProcessor | `points-earned` | the customer's points and promotions | `customer-promoted` at 1,000 points, or nothing |
| CustomerProfileProjector | `customer-promoted` | | the `customer-profile` projection |
| OrderSummaryProjector | `order-placed` | | the `order-summary` projection |
| ReserveStockModel, run by StockProcessor | `order-placed` | the product's stock | `stock-reserved`, or a refusal |
| RequestRestockModel, run by RestockProcessor | `stock-reserved` | the product's stock and restocks | `restock-requested` below 3 units, or nothing |
| PurchasingQueueProjector | `restock-requested` | | the `purchasing-queue` projection |
| StockLevelProjector | `stock-reserved` | | the `stock-level` projection |

The application runs the code that reacts to an event one piece at a time, in alphabetical order. When a piece writes
events, the code that reacts to them runs before the next piece. The order is the application's choice: TamarackDB has
no say in it.

## What the store holds

Customer `c1` has 950 points. Product `p1` received 10 units and has 7 reserved: 3 are left. The customer orders one
unit of `p1`, for 30 dollars.

In the calls below, `{txId}` stands for the transaction ID, and each response follows its request. Payloads are short
JSON strings.

## The order

**PlaceOrderModel** begins the transaction, reads the customer, and places the order.

```
POST /tx
200 {"txId":"7d1e4b2a-3c5f-4e6d-9a8b-0c1d2e3f4a5b"}

QUERY /tx/{txId}/events
{"query":[{"types":["customer-registered"],"identifiers":[{"name":"customerId","value":"c1"}]}]}
200
{"sequence":1,"time":"2026-09-02T08:00:00.000000Z","type":"customer-registered","identifiers":{"customerId":"c1"},"metadata":{},"payload":"{}"}
{"end":true}

POST /tx/{txId}/events
{"events":[{"type":"order-placed","identifiers":{"orderId":"o1","customerId":"c1","productId":"p1"},"payload":"{\"quantity\":1,\"total\":30}"}]}
200 {"time":"2026-10-03T21:11:05.123456Z"}
```

**LoyaltyProcessor** reacts to `order-placed`. Its **EarnPointsModel** depends on no event, so it reads `"none"`.

```
QUERY /tx/{txId}/events
{"query":"none"}
200
{"end":true}

POST /tx/{txId}/events
{"events":[{"type":"points-earned","identifiers":{"customerId":"c1","orderId":"o1"},"payload":"{\"points\":30}"}]}
200 {"time":"2026-10-03T21:11:05.140210Z"}
```

**CustomerPointsProjector** reacts to `points-earned`, before the next piece of code that reacts to `order-placed`.

```
GET /tx/{txId}/projections/customer-points/c1
200 {"points":950}

POST /tx/{txId}/projections
{"upsert":[{"type":"customer-points","id":"c1","payload":"{\"points\":980}"}]}
200 {"time":"2026-10-03T21:11:05.151877Z"}
```

**TierProcessor** reacts to `points-earned` too. Its **PromoteCustomerModel** sees the committed points, then the
pending ones, with no `sequence`. 980 points is below the threshold: it writes nothing, and the empty write still
closes its read. No `customer-promoted` event exists, so CustomerProfileProjector doesn't run.

```
QUERY /tx/{txId}/events
{"query":[{"types":["points-earned","customer-promoted"],"identifiers":[{"name":"customerId","value":"c1"}]}]}
200
{"sequence":40,"time":"2026-09-20T10:15:00.000000Z","type":"points-earned","identifiers":{"customerId":"c1"},"metadata":{},"payload":"{\"points\":950}"}
{"time":"2026-10-03T21:11:05.140210Z","type":"points-earned","identifiers":{"customerId":"c1","orderId":"o1"},"metadata":{},"payload":"{\"points\":30}"}
{"end":true}

POST /tx/{txId}/events
{"events":[]}
200 {"time":"2026-10-03T21:11:05.160032Z"}
```

**OrderSummaryProjector** reacts to `order-placed`, long after PlaceOrderModel wrote it. That's fine: a projection
always follows a decision already made.

```
GET /tx/{txId}/projections/order-summary/o1
404 {"error":"ProjectionNotFound"}

POST /tx/{txId}/projections
{"upsert":[{"type":"order-summary","id":"o1","payload":"{\"status\":\"placed\",\"total\":30}"}]}
200 {"time":"2026-10-03T21:11:05.171590Z"}
```

**StockProcessor** reacts to `order-placed`. Its **ReserveStockModel** finds 3 units left, and reserves one.

```
QUERY /tx/{txId}/events
{"query":[{"types":["stock-received","stock-reserved"],"identifiers":[{"name":"productId","value":"p1"}]}]}
200
{"sequence":2,"time":"2026-09-02T08:05:00.000000Z","type":"stock-received","identifiers":{"productId":"p1"},"metadata":{},"payload":"{\"quantity\":10}"}
{"sequence":45,"time":"2026-09-28T16:40:00.000000Z","type":"stock-reserved","identifiers":{"productId":"p1","orderId":"o0"},"metadata":{},"payload":"{\"quantity\":7}"}
{"end":true}

POST /tx/{txId}/events
{"events":[{"type":"stock-reserved","identifiers":{"productId":"p1","orderId":"o1"},"payload":"{\"quantity\":1}"}]}
200 {"time":"2026-10-03T21:11:05.183305Z"}
```

**RestockProcessor** reacts to `stock-reserved`. Its **RequestRestockModel** sees 2 units left, below 3, and asks for
more.

```
QUERY /tx/{txId}/events
{"query":[{"types":["stock-received","stock-reserved","restock-requested"],"identifiers":[{"name":"productId","value":"p1"}]}]}
200
{"sequence":2,"time":"2026-09-02T08:05:00.000000Z","type":"stock-received","identifiers":{"productId":"p1"},"metadata":{},"payload":"{\"quantity\":10}"}
{"sequence":45,"time":"2026-09-28T16:40:00.000000Z","type":"stock-reserved","identifiers":{"productId":"p1","orderId":"o0"},"metadata":{},"payload":"{\"quantity\":7}"}
{"time":"2026-10-03T21:11:05.183305Z","type":"stock-reserved","identifiers":{"productId":"p1","orderId":"o1"},"metadata":{},"payload":"{\"quantity\":1}"}
{"end":true}

POST /tx/{txId}/events
{"events":[{"type":"restock-requested","identifiers":{"productId":"p1"},"payload":"{\"quantity\":10}"}]}
200 {"time":"2026-10-03T21:11:05.195548Z"}
```

**PurchasingQueueProjector** reacts to `restock-requested`, then **StockLevelProjector** reacts to `stock-reserved`.

```
GET /tx/{txId}/projections/purchasing-queue/p1
404 {"error":"ProjectionNotFound"}

POST /tx/{txId}/projections
{"upsert":[{"type":"purchasing-queue","id":"p1","payload":"{\"quantity\":10}"}]}
200 {"time":"2026-10-03T21:11:05.204411Z"}

GET /tx/{txId}/projections/stock-level/p1
200 {"available":3}

POST /tx/{txId}/projections
{"upsert":[{"type":"stock-level","id":"p1","payload":"{\"available\":2}"}]}
200 {"time":"2026-10-03T21:11:05.213907Z"}
```

Every piece of code has run: the application commits.

```
POST /tx/{txId}/commit
204
```

The four events are now in the log, each with the `time` of its write, and four projections are written. The
transaction read events five times, so it carried five conditions, `conditions[0]` to `conditions[4]`, in that order.

## When something changes in between

**The last unit.** Say another order for `p1` committed a `stock-reserved` after ReserveStockModel read the stock. The
commit is refused, and nothing is written:

```
POST /tx/{txId}/commit
409 {"error":"ConcurrencyException","message":"conditions[3] no longer holds"}
```

The application runs the whole command again, from `POST /tx`. If ReserveStockModel now finds no unit left, it refuses,
and the application abandons the transaction. The order never exists.

```
DELETE /tx/{txId}
204
```

**Points from elsewhere.** Say a referral, in another transaction, added 40 points for `c1` and committed first. The
empty write of PromoteCustomerModel is checked like any decision, so the commit gets `conditions[2] no longer holds`.
The command runs again, PromoteCustomerModel sees 1,020 points, and writes `customer-promoted` (see
[Transactions](/docs/concepts/transactions/#one-decision-one-read-one-write)).

## A projector that catches up: sales by day

A projector that makes no decision doesn't use a transaction. DailySalesProjector keeps one `daily-sales` projection per
day, from the `order-placed` events. It keeps its position, a store ID and the last Sequence Position processed, in a
projection of its own, `projector-position/daily-sales`. It runs in a loop, one page at a time, and writes with
[`POST /projections`](/docs/http-api/projections/#writing-projections).

1. It reads its position, and keeps its version:

   ```
   GET /projections/projector-position/daily-sales
   200
   X-Tamarackdb-Version: 1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6
   {"store":"5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47","sequence":120}
   ```

   A `404` means it never ran: it starts from the beginning, and its position will be a `create`.
2. It reads the next page of orders:

   ```
   QUERY /events
   {"query":[{"types":["order-placed"]}],"afterSequence":120,"limit":500}
   ```

   If the `X-Tamarackdb-Store` header isn't the store ID of its position, the store was reset: it starts from the
   beginning.
3. For each day the page touches, it reads the day's projection and its version, or gets a `404`.
4. It writes the days and its new position in one write, each with the version it read:

   ```
   POST /projections
   {
     "create":[{"type":"daily-sales","id":"2026-10-03","payload":"{\"orders\":1,\"total\":30}"}],
     "replace":[{"type":"projector-position","id":"daily-sales","version":"1b2c3d4e-5f60-4718-9a0b-c1d2e3f4a5b6","payload":"{\"store\":\"5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47\",\"sequence\":187}"}]
   }
   200
   {"create":[{"version":"5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d"}],"replace":[{"version":"d4c3b2a1-0f9e-4d8c-b7a6-5f4e3d2c1b0a"}]}
   ```

   It keeps the new version of its position for the next page.

5. A `409` means another instance, or a rebuild, changed the position or a day since it read them. Nothing was written:
   it starts over at step 1.
6. If the page's trailer said `{"hasMore": true}`, it goes on with the next page. Otherwise it waits a moment, and
   starts over.

- The position and the days are written together: a page is processed once, never skipped or repeated, even after a
  crash between two pages.
- Two instances that cross get a `409` on the position. The server doesn't know which projector writes what: the
  version keeps them apart.
- A reset between its read and its write deletes the position, so the `replace` of the position gets `409`: days
  computed from the old store are never written to the new one. This is one reason the position goes in the same
  `POST /projections` as the days.
- The projection may use `sequence` and `time`: the events it reads are already committed (see
  [Projections](/docs/concepts/projections/#what-a-projection-may-use)).
- A rebuild deletes the `daily-sales` projections with a [bulk delete](/docs/http-api/projections/#bulk-delete), and
  the position with `POST /projections`. The projector then starts from the beginning.
