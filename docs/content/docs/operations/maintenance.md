---
title: "Maintenance"
description: "The two maintenance tasks left to you, both run by hand while the server is stopped: giving back disk space with VACUUM, and a full ANALYZE."
slug: "maintenance"
weight: 8
---

The server maintains itself: SQLite checkpoints its WAL, and the server refreshes query statistics every hour. Two
operations are left to you, both run by hand while the server is
stopped.

Run both as the server's user (see [Install](/docs/operations/install/#run)).

## Giving back disk space

SQLite reuses the space of deleted projections on its own, so the file doesn't keep growing after a
[rebuild](/docs/concepts/projections/#rebuilds). To give that space back to the operating system, run a `VACUUM`. The
server never runs one itself.

1. Stop `tamarackdb-server`, at a time the application can be down, for example at the end of a rebuild.
2. Run the `VACUUM`:

   ```sh
   sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'VACUUM;'
   ```

3. Start `tamarackdb-server` again.

A `VACUUM` rewrites the whole file, events included, and needs free disk space about the size of the database while
it runs.

## Refreshing query statistics

After a one-off bulk import, run a full `ANALYZE` the same way:

```sh
sudo -u tamarackdb sqlite3 /path/to/data/tamarackdb.sqlite 'ANALYZE;'
```

**Why the server is stopped.** Stopping it costs a few seconds, and keeps the server free of a long operation it would
have to coordinate with reads in flight. A WAL file left behind by another user is also one the server can't open.
