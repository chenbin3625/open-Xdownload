#!/bin/sh
set -eu
umask 077
if [ "$(id -u)" = "0" ]; then
    mkdir -p /data /config
    chown archive:archive /data /config
    chmod 700 /data /config
    exec gosu archive "$@"
fi
exec "$@"
