#!/bin/sh
# Creates the database on the first start, when the volume on /data holds
# none, then runs the server. Outside Docker, tamarackdb-init is a separate
# step, and the server refuses to start without a database.
set -e

if [ ! -e "$TAMARACKDB_DATA_DIR/tamarackdb.sqlite" ]; then
    ./tamarackdb-init --data-dir "$TAMARACKDB_DATA_DIR"
fi

exec ./tamarackdb-server "$@"
