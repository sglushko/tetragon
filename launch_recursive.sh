#!/usr/bin/env bash
set -euo pipefail

# Usage: ./launch_recursive.sh [count]
# Default count is 1.

COUNT=${1:-1}

echo "Starting $COUNT runs of recursive workload..."

for ((i=1; i<=COUNT; i++)); do
    echo "----------------------------------------------------------------"
    echo "Run $i/$COUNT"
    echo "----------------------------------------------------------------"
    
    ENABLE_MEMORY_CHECK=1 \
    LONG_LIFETIME_SEC=30 \
    SHORT_LIFETIME_SEC=1 \
    CHAIN_DEPTH=98 \
    ./run_workload.sh workload_recursive.pl 100
    
    echo "Run $i completed."
    # Optional sleep between runs if needed
    # sleep 1
done

echo "All $COUNT runs finished."
