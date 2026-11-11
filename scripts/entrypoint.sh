#!/bin/sh
set -eu
umask 077
if [ "$(id -u)" = "0" ]; then
    if [ "${1:-}" = "x-archive" ]; then
        data_dir="${X_ARCHIVE_DATA:-data}"
        config_dir="${X_ARCHIVE_CONFIG:-config}"
        next=""
        for arg in "$@"; do
            case "$next" in
                data) data_dir="$arg"; next=""; continue ;;
                config) config_dir="$arg"; next=""; continue ;;
                skip) next=""; continue ;;
            esac
            case "$arg" in
                --data-dir) next=data ;;
                --config-dir) next=config ;;
                --data-dir=*) data_dir="${arg#*=}" ;;
                --config-dir=*) config_dir="${arg#*=}" ;;
                --bind|--password) next=skip ;;
            esac
        done
        mkdir -p -- "$data_dir" "$config_dir"
        chown archive:archive -- "$data_dir" "$config_dir"
        chmod 700 -- "$data_dir" "$config_dir"
    fi
    exec gosu archive "$@"
fi
exec "$@"
