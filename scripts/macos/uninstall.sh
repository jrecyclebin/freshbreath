#!/bin/bash
# Remove Fresh Breath. Run with sudo (the daemon and its files are system-level):
#
#   sudo /usr/local/freshbreath/uninstall.sh
#
# Data (the database, uploaded apps, logs) is intentionally left in place —
# uninstalling shouldn't destroy the user's data, same as the Windows
# uninstaller. The _freshbreath service user and group are left in place too; removing a
# system user automatically is the kind of thing that surprises people.
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "uninstall.sh: run with sudo" >&2; exit 1; }

LABEL=institute.poggers.freshbreath
PLIST=/Library/LaunchDaemons/${LABEL}.plist
DATA_DIR="/Library/Application Support/freshbreath"

# Stop and unload the daemon before removing its plist and binary.
launchctl bootout system/"$LABEL" 2>/dev/null || true
rm -f "$PLIST"
rm -rf /usr/local/freshbreath

cat <<EOF
Fresh Breath removed.
  Data left in place at $DATA_DIR (DB, apps, logs).
    Remove it with: sudo rm -rf "$DATA_DIR"
  Service user _freshbreath left in place.
    Remove it with: sudo dscl . -delete /Users/_freshbreath
                    sudo dscl . -delete /Groups/_freshbreath
EOF
