#!/usr/bin/env bash
set -euo pipefail

# Simple load generator for process-cache reference counting issues.
# It continuously starts Perl scripts via bash -c. Each Perl script
# forks a child and exits immediately, while the child sleeps for a
# short period. This exercises fork/exec and parent/child lifetime
# handling for script interpreters.

DURATION_SECONDS="${1:-300}"
end_ts=$((SECONDS + DURATION_SECONDS))

count=0
last_print=$SECONDS

while (( SECONDS < end_ts )); do
  # Start a Perl script via a subshell. The script forks and the parent
  # exits immediately; the child busy-waits for ~1 second (CPU load)
  # and then exits.
  bash -c "perl -e 'my \$pid = fork(); if (!defined \$pid) { exit 1 } if (\$pid == 0) { my \$end = time + 1; my \$x = 0; while (time < \$end) { for (1..100000) { \$x++ } } exit 0 } exit 0'" &
  count=$((count + 1))

  # Once per second, print how many launches have happened so far
  # and how many seconds are left until the script finishes.
  if (( SECONDS > last_print )); then
    remaining=$((end_ts - SECONDS))
    if (( remaining < 0 )); then
      remaining=0
    fi
    printf 'launches=%d remaining=%ds\n' "$count" "$remaining"
    last_print=$SECONDS
  fi

  # Small sleep to avoid overwhelming the system completely.
  # sleep 0.001
done

wait || true
