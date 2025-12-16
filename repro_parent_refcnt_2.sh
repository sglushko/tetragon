#!/usr/bin/env bash
set -euo pipefail

# Load generator for long-lived parent/child and deep recursive
# process trees. It repeatedly launches the Perl reproducer, which
# chooses one of several scenarios (long-lived parent, long-lived
# child, deep chains with dozens of levels).

DURATION_SECONDS="${1:-300}"
end_ts=$((SECONDS + DURATION_SECONDS))

count=0
last_print=$SECONDS

while (( SECONDS < end_ts )); do
  perl ./repro_parent_refcnt_2.pl &
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

  # Optional small sleep to avoid exhausting system resources too fast.
  sleep 0.05
done

wait || true
