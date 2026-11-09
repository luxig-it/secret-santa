#!/bin/sh
# Nightly backup: a consistent copy of the database while the app keeps
# running, kept for 30 days. Run from cron, e.g.
#   15 3 * * *  /opt/secret-santa/backup.sh
# (a script, because cron would cut an inline `date +%F` at the "%").
set -eu
cd "$(dirname "$0")"
mkdir -p backups
name="backups/secret-santa-$(date +%Y-%m-%d-%H%M).db"
docker compose exec -T app /secret-santa backup > "$name.part"
mv "$name.part" "$name"
find backups -name 'secret-santa-*.db' -mtime +30 -delete
