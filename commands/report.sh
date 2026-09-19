#!/usr/bin/env bash
# Example command script: a system report assembled from pipes.
# Register with: ./scripts/manage_commands.sh add "System Report" --script report.sh \
#   --template '📊 System report:\n${output}'
set -euo pipefail

echo "Load:   $(cat /proc/loadavg)"
echo "Disk /: $(df -h / | tail -1)"
echo "Memory: $(free -h | sed -n 2p | awk '{print $3"/"$2}')"