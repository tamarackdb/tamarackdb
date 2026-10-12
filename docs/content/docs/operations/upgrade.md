---
title: "Upgrade"
description: "Moving a TamarackDB instance to a new release: replacing the binaries, migrating the database to a new major version with tamarackdb-migrate."
slug: "upgrade"
weight: 7
---

Moving an instance to a new release. Only a new major version changes the database file, and `tamarackdb-migrate`
brings the file to it.

## Within a major version

1. Stop the server.
2. Replace the binaries (see [Install](/docs/operations/install/#binaries)).
3. Start the server.

## To a new major version

1. Stop the server, and stop scheduling `tamarackdb-backup`.
2. Copy the database file, as a backup. `tamarackdb-migrate` makes none.
3. Replace the binaries.
4. Migrate the database file, as the server's user:

   ```sh
   sudo -u tamarackdb /usr/local/bin/tamarackdb-migrate --data-dir /var/lib/tamarackdb
   ```

   ```text
   tamarackdb-migrate: migrated /var/lib/tamarackdb/tamarackdb.sqlite from schema version 1 to 2
   ```

5. Start the server. When the release notes say the migration deletes the projections, restart every projector from
   the beginning, with the application stopped.
6. Point `tamarackdb-backup` to a new, empty `dataDir`, and schedule it again.

- The schema version of a database is the major version of TamarackDB.
- The server refuses to start on a database at another version.
- `tamarackdb-migrate` refuses to run while the server, or any other TamarackDB process, uses the file.
- Each `tamarackdb-migrate` migrates from the previous major version only. To skip major versions, run each
  intermediate `tamarackdb-migrate`, in order.
- A database already at the new version is left as it is.
- The migration is all or nothing: when it fails, the file stays at the previous version.
- There is no way back to an older version, other than the copy made before the migration.

## Docker

Stop the container, then run `tamarackdb-migrate` from the new image, on the same volume:

```sh
docker run --rm -v tamarackdb-data:/data --entrypoint ./tamarackdb-migrate \
  ghcr.io/tamarackdb/tamarackdb:latest --data-dir /data
```

## Restoring a backup

- To restore a backup made with an older major version, copy it into the new data directory (see
  [Backup and Import](/docs/operations/backup/#restoring)), then run `tamarackdb-migrate` on that directory.
