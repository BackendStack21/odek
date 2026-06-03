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

if [ -f "$CRONTAB" ]; then
	echo "cron-entrypoint: starting supercronic for $CRONTAB" >&2
	# -passthrough-logs keeps each job's own stdout/stderr intact in the
	# container log alongside supercronic's scheduling lines.
	supercronic -passthrough-logs "$CRONTAB" &
else
	echo "cron-entrypoint: no crontab at $CRONTAB — skipping supercronic" >&2
fi

# Default to printing usage if no command was provided (matches the previous
# `ENTRYPOINT ["odek"]` behaviour for a bare `docker run`).
exec odek "$@"
