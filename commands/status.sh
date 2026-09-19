#!/usr/bin/env bash
# Example command script: quick system status.
# Register with: ./scripts/manage_commands.sh add "System Status" --script status.sh
set -euo pipefail

echo "uptime: $(uptime -p)"
echo "date:   $(date '+%a %b %d %H:%M:%S')"
echo "user:   $(whoami)@$(hostname)"