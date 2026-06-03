#!/bin/sh
# cron-entrypoint.sh — container entrypoint for the odek image.
#
# If a crontab is mounted, start supercronic in the background, then hand the
# container over to the real odek command (serve / telegram / run / …) via
# `exec` so odek remains the main process: signals, graceful restart, and the
# Telegram singleton lock all behave exactly as they did before this wrapper.
#
# supercronic inherits THIS process's environment and passes it to every cron
# job, so a scheduled `odek run --deliver` sees the same env_file vars
# (ODEK_API_KEY, ODEK_TELEGRAM_BOT_TOKEN, ODEK_TELEGRAM_DEFAULT_CHAT_ID, …)
# that the bot does. That is the whole reason for using supercronic over the
# classic crond, which scrubs the environment from its jobs.
set -eu

# Path to the crontab. Overridable so an operator can relocate the mount.
CRONTAB="${ODEK_CRONTAB:-/home/odek/.crontabs/crontab}"

# Graceful-restart support: odek's /restart command re-execs via this script
# (see ODEK_ENTRYPOINT below). Kill any supercronic from the previous run so we
# never end up with two instances scheduling the same crontab concurrently.
if [ -n "${ODEK_SUPERCRONIC_PID:-}" ]; then
    kill "$ODEK_SUPERCRONIC_PID" 2>/dev/null || true
    unset ODEK_SUPERCRONIC_PID
fi

if [ -d "$CRONTAB" ]; then
    # Docker creates a directory when the bind-mount source doesn't exist on the
    # host. This is almost always a misconfiguration — warn loudly rather than
    # silently skipping so the operator knows why reminders aren't firing.
    echo "cron-entrypoint: WARNING: $CRONTAB is a directory, not a file" >&2
    echo "cron-entrypoint: Docker created it because the host path was missing." >&2
    echo "cron-entrypoint: Fix: remove the directory on the host and create the file." >&2
    echo "cron-entrypoint: Skipping supercronic — cron jobs will NOT run." >&2
elif [ -f "$CRONTAB" ]; then
    echo "cron-entrypoint: starting supercronic for $CRONTAB" >&2
    # -passthrough-logs keeps each job's own stdout/stderr intact in the
    # container log alongside supercronic's scheduling lines.
    supercronic -passthrough-logs "$CRONTAB" &
    ODEK_SUPERCRONIC_PID=$!
    export ODEK_SUPERCRONIC_PID
    # Brief liveness check: supercronic parses the crontab at startup and exits
    # immediately on a syntax error or missing binary. Neither is recoverable at
    # runtime, so catching it here produces a clear warning rather than silent
    # non-delivery. set -e does not cover backgrounded processes, so we check
    # explicitly after a short window.
    sleep 1
    if ! kill -0 "$ODEK_SUPERCRONIC_PID" 2>/dev/null; then
        echo "cron-entrypoint: WARNING: supercronic exited immediately — cron jobs will NOT run" >&2
        unset ODEK_SUPERCRONIC_PID
    fi
else
    echo "cron-entrypoint: no crontab at $CRONTAB — skipping supercronic" >&2
fi

# Advertise this script's own path so spawnChild (odek's /restart handler) can
# re-exec through the wrapper instead of the bare binary. Without this, a
# graceful restart would skip supercronic entirely.
export ODEK_ENTRYPOINT="$0"

# Default to printing usage if no command was provided (matches the previous
# `ENTRYPOINT ["odek"]` behaviour for a bare `docker run`).
exec odek "$@"
