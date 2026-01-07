#!/usr/bin/env bash

# Disable set -e to prevent silent exits. We handle errors manually.
# set -euo pipefail

MAX_RECV_SIZE=${MAX_RECV_SIZE:-1000000000}
NAME_PREFIX=${NAME_PREFIX:-tetragon}
HOST_OUT_DIR=${HOST_OUT_DIR:-./processcache_dumps}
INTERVAL_SEC=${INTERVAL_SEC:-5}
EXEC_TIMEOUT_SEC=${EXEC_TIMEOUT_SEC:-10}

# Ensure output directory exists
mkdir -p "$HOST_OUT_DIR"

echo "Starting check_workload_dump.sh..."
echo "HOST_OUT_DIR=$HOST_OUT_DIR"

run_timeout() {
  local timeout_sec="$1" outfile="$2" rcfile="$3"
  shift 3

  : >"$outfile"
  : >"$rcfile"

  ("$@" >"$outfile" 2>/dev/null; echo $? >"$rcfile") &
  local pid=$!
  local start=$SECONDS

  while kill -0 "$pid" 2>/dev/null; do
    if (( SECONDS - start >= timeout_sec )); then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      echo 124 >"$rcfile"
      return 124
    fi
    sleep 1
  done

  wait "$pid" 2>/dev/null || true
  local rc
  rc=$(cat "$rcfile" 2>/dev/null || echo 1)
  return "$rc"
}

calc_pct() {
  local val=$1
  local total=$2
  if (( total == 0 )); then
    echo "0.00%"
  else
    awk -v v="$val" -v t="$total" 'BEGIN {printf "%.2f%%", (v/t)*100}'
  fi
}

if ! command -v docker >/dev/null 2>&1; then
  echo "error: docker not found"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq not found"
  exit 1
fi

while true; do
  echo "---------------------------------------------------"
  echo "LOCAL_TIME: $(date)"
  echo "listing containers..."

  ps_out=$(mktemp)
  ps_rc=$(mktemp)
  
  run_timeout "$EXEC_TIMEOUT_SEC" "$ps_out" "$ps_rc" docker ps --format '{{.Names}}'
  rc=$(cat "$ps_rc" 2>/dev/null || echo 1)
  
  if [[ "$rc" != "0" ]]; then
    rm -f "$ps_out" "$ps_rc"
    echo "warning: docker ps failed/timeout rc=${rc}"
    sleep "$INTERVAL_SEC"
    continue
  fi
  rm -f "$ps_rc"

  containers=()
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    containers+=("$name")
  done < <(grep -E "^${NAME_PREFIX}[0-9]*$" "$ps_out" || true)
  rm -f "$ps_out"

  echo "found ${#containers[@]} containers matching '${NAME_PREFIX}'"

  if (( ${#containers[@]} == 0 )); then
    echo "no running containers matching ^${NAME_PREFIX}[0-9]*$"
    sleep "$INTERVAL_SEC"
    continue
  fi

  for c in "${containers[@]}"; do
    echo "[$c] dumping processcache..."

    if ! docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -q '^true$'; then
      echo "[$c] skipped (not running)"
      continue
    fi

    tmp_cache=$(mktemp)
    dump_rc=$(mktemp)
    
    run_timeout "$EXEC_TIMEOUT_SEC" "$tmp_cache" "$dump_rc" docker exec "$c" bash -lc "tetra dump processcache --exclude-execve-map-processes --skip-zero-refcnt --max-recv-size ${MAX_RECV_SIZE}"
    rc=$(cat "$dump_rc" 2>/dev/null || echo 1)
    
    if [[ "$rc" != "0" ]]; then
      rm -f "$tmp_cache" "$dump_rc"
      echo "[$c] warning: failed to dump processcache (rc=$rc)"
      continue
    fi
    rm -f "$dump_rc"

    record_count=$(wc -l < "$tmp_cache" | tr -d ' ')
    
    ts=$(date +'%Y%m%d_%H%M%S')
    found_dump="${HOST_OUT_DIR}/CHECK_${ts}_${c}_${record_count}.json"
    mv "$tmp_cache" "$found_dump"

    echo "[$c] cache saved: ${found_dump}"

    # --- Analysis ---
    
    # 2. Refcnt > 0
    ops_list=$(mktemp)
    
    # Extract ops keys for refcnt > 0
    # JQ keys are sorted alphabetically: parent++, parent--, process++, process--
    # We output them as a single line per record
    if ! jq -r 'select(.refcnt > 0) | .refcnt_ops | keys | join(" ")' "$found_dump" > "$ops_list"; then
        echo "[$c] error: jq failed to parse dump"
        rm -f "$ops_list"
        continue
    fi
    
    total_refcnt_gt_0=$(wc -l < "$ops_list" | tr -d ' ')
    
    # Calculate categories (subsets of refcnt > 0)
    # 1. only "process++"
    c1=$(grep -cx "process++" "$ops_list" || true)
    
    # 2. only "parent++" and "process++" (sorted)
    c2=$(grep -cx "parent++ process++" "$ops_list" || true)
    
    # 3. "parent++", "process++", "process--" (sorted)
    c3=$(grep -cx "parent++ process++ process--" "$ops_list" || true)
    
    # 4. "parent++", "process++", "parent--" -> sorted: "parent++ parent-- process++"
    c4=$(grep -cx "parent++ parent-- process++" "$ops_list" || true)

    # 5. "parent++", "parent--", "process++", "process--" (sorted)
    c5=$(grep -cx "parent++ parent-- process++ process--" "$ops_list" || true)

    c_other=$(( total_refcnt_gt_0 - c1 - c2 - c3 - c4 - c5 ))

    echo "[$c] Stats:"
    echo "  Total Records: ${record_count}"
    echo "  Refcnt > 0:    ${total_refcnt_gt_0} ($(calc_pct "$total_refcnt_gt_0" "$record_count"))"
    
    if (( total_refcnt_gt_0 > 0 )); then
        echo "  Breakdown of Refcnt > 0 (percentages relative to Total Records):"
        echo "    1. [process++] only:                      ${c1} ($(calc_pct "$c1" "$record_count"))"
        echo "    2. [parent++, process++] only:            ${c2} ($(calc_pct "$c2" "$record_count"))"
        echo "    3. [parent++, process++, process--] only: ${c3} ($(calc_pct "$c3" "$record_count"))"
        echo "    4. [parent++, process++, parent--] only:  ${c4} ($(calc_pct "$c4" "$record_count"))"
        echo "    5. [parent++, parent--, process++, process--] only: ${c5} ($(calc_pct "$c5" "$record_count"))"
        echo "    6. Other combinations:                    ${c_other} ($(calc_pct "$c_other" "$record_count"))"
    fi
    
    rm -f "$ops_list"
  done

  sleep "$INTERVAL_SEC"
done
