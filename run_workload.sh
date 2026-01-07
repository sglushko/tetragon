#!/usr/bin/env bash
set -euo pipefail

# Load generator for long-lived parent/child and deep recursive
# process trees. It repeatedly launches the Perl reproducer, which
# chooses one of several scenarios (long-lived parent, long-lived
# child, deep chains with dozens of levels).

WORKLOAD_SCRIPT="${1:?usage: $0 <workload_script.pl> [duration_seconds]}"
DURATION_SECONDS="${2:-300}"
end_ts=$((SECONDS + DURATION_SECONDS))

# Thresholds for pausing launches when memory usage is high.
# These are ratios of the cgroup limit (e.g. 90 -> pause when >=90% of limit).
MEMORY_HIGH_WATER_PCT=${MEMORY_HIGH_WATER_PCT:-90}
MEMORY_RESUME_PCT=${MEMORY_RESUME_PCT:-85}
MEMORY_CHECK_INTERVAL=${MEMORY_CHECK_INTERVAL:-1}

# Optional override for memory.max when running without a cgroup limit.
# Accepts raw bytes, arithmetic expressions (e.g. 10*1024*1024), or IEC suffixes (e.g. 1GiB).
MEMORY_LIMIT_BYTES=${MEMORY_LIMIT_BYTES:-$((20 * 1024 * 1024 * 1024))}

# Optional throttle based on free memory reported by `free -b` (field 4).
# Example: FREE_MEM_MIN_BYTES=1GiB
FREE_MEM_MIN_BYTES=${FREE_MEM_MIN_BYTES:-$((0 * 1024 * 1024 * 1024))}

count=0
last_print=$SECONDS
warned_no_cgroup=0

fmt_bytes() {
  local v="$1"
  if command -v numfmt >/dev/null 2>&1; then
    numfmt --to=iec "$v"
  else
    printf '%sB' "$v"
  fi
}

read_cgroup_memory() {
  local usage_file limit_file usage limit

  if [[ -r /sys/fs/cgroup/memory.current && -r /sys/fs/cgroup/memory.max ]]; then
    usage_file=/sys/fs/cgroup/memory.current
    limit_file=/sys/fs/cgroup/memory.max
  elif [[ -r /sys/fs/cgroup/memory/memory.usage_in_bytes && -r /sys/fs/cgroup/memory/memory.limit_in_bytes ]]; then
    usage_file=/sys/fs/cgroup/memory/memory.usage_in_bytes
    limit_file=/sys/fs/cgroup/memory/memory.limit_in_bytes
  else
    echo "warning: read_cgroup_memory" >&2
    return 1
  fi

  usage=$(<"$usage_file")
  limit=$(<"$limit_file")

  if [[ -n "$MEMORY_LIMIT_BYTES" && ( "$limit" == "max" || "$limit" == "0" ) ]]; then
    limit=$MEMORY_LIMIT_BYTES
  fi

  printf '%s %s\n' "$usage" "$limit"
}

read_free_memory_bytes() {
  if ! command -v free >/dev/null 2>&1; then
    echo "warning: free command not found" >&2
    return 1
  fi

  free -b | awk 'NR==2{print $4; exit}'
}

parse_usage_limit() {
  local data="$1" usage limit
  usage=${data%% *}
  limit=${data##* }
  printf '%s %s\n' "$usage" "$limit"
}

ensure_memory_available() {
  local data usage limit stop resume

  if (( FREE_MEM_MIN_BYTES > 0 )); then
    local free_mem
    free_mem=$(read_free_memory_bytes || true)
    if [[ -n "${free_mem}" && "${free_mem}" =~ ^[0-9]+$ ]]; then
      if (( free_mem < FREE_MEM_MIN_BYTES )); then
        echo "free memory low ($(fmt_bytes "$free_mem") < $(fmt_bytes "$FREE_MEM_MIN_BYTES")), pausing launches..." >&2
        while (( free_mem < FREE_MEM_MIN_BYTES )); do
          sleep "$MEMORY_CHECK_INTERVAL"
          free_mem=$(read_free_memory_bytes || true)
          if [[ -z "${free_mem}" || ! "${free_mem}" =~ ^[0-9]+$ ]]; then
            break
          fi
        done
        if [[ -n "${free_mem}" && "${free_mem}" =~ ^[0-9]+$ ]]; then
          echo "free memory recovered ($(fmt_bytes "$free_mem")), resuming launches" >&2
        fi
      fi
    fi
  fi

  if ! data=$(read_cgroup_memory); then
    if (( warned_no_cgroup == 0 )); then
      echo "warning: could not read cgroup memory usage; skipping memory throttling" >&2
      warned_no_cgroup=1
    fi
    return
  fi

  read -r usage limit < <(parse_usage_limit "$data")

  if [[ "$limit" == "max" || "$limit" == "0" ]]; then
    return
  fi

  stop=$(( limit * MEMORY_HIGH_WATER_PCT / 100 ))
  resume=$(( limit * MEMORY_RESUME_PCT / 100 ))

  if (( stop <= resume )); then
    resume=$(( stop - (limit / 100) ))
    if (( resume < 0 )); then
      resume=0
    fi
  fi

  if (( usage >= stop )); then
    echo "memory usage high ($(fmt_bytes "$usage") / $(fmt_bytes "$limit")), pausing launches..." >&2
    while (( usage > resume )); do
      sleep "$MEMORY_CHECK_INTERVAL"
      data=$(read_cgroup_memory) || break
      read -r usage limit < <(parse_usage_limit "$data")
      if [[ "$limit" == "max" || "$limit" == "0" ]]; then
        break
      fi
    done
    echo "memory usage back under control ($(fmt_bytes "$usage")), resuming launches" >&2
  fi
}

while (( SECONDS < end_ts )); do
  if [[ -n "${ENABLE_MEMORY_CHECK:-}" ]]; then
    ensure_memory_available
  fi

  perl "./$WORKLOAD_SCRIPT" &
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
  #sleep 0.01
done

wait || true

