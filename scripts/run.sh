#!/bin/bash
set -e

if [[ "${UPTIME_KUMA_DB_TYPE}" == 'mariadb' ]]; then
	node server/server.js
else
	# Restore the database if it does not already exist.
	if [[ -f "${DB_PATH}" ]]; then
		echo "Database already exists, skipping restore"
	else
		echo "No database found, restoring from replica if exists"
		litestream restore -if-replica-exists -o "${DB_PATH}" "${REPLICA_URL}"
	fi
	# Run litestream with your app as the subprocess.
	exec litestream replicate -exec "node server/server.js"
fi
