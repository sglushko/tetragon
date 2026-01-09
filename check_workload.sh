#!/usr/bin/env bash
set -euo pipefail

MAX_RECV_SIZE=${MAX_RECV_SIZE:-1000000000}
NAME_PREFIX=${NAME_PREFIX:-tetragon}
CONTAINER_CACHE_PATH=${CONTAINER_CACHE_PATH:-/tmp/cache.json}
HOST_OUT_DIR=${HOST_OUT_DIR:-./processcache_dumps}
INTERVAL_SEC=${INTERVAL_SEC:-5}
REFCNT_GREP=${REFCNT_GREP:-'"refcnt":4294'}
EXEC_TIMEOUT_SEC=${EXEC_TIMEOUT_SEC:-10}

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

docker_exec_timeout() {
  local container="$1" timeout_sec="$2" outfile="$3" rcfile="$4"
  shift 4

  : >"$outfile"
  : >"$rcfile"

  run_timeout "$timeout_sec" "$outfile" "$rcfile" docker exec "$container" "$@"
}

if ! command -v docker >/dev/null 2>&1; then
  echo "error: docker not found" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq not found" >&2
  exit 1
fi

mkdir -p "$HOST_OUT_DIR"

while true; do
  echo "LOCAL_TIME: $(date)" >&2
  echo "listing containers..." >&2

  ps_out=$(mktemp)
  ps_rc=$(mktemp)
  if ! run_timeout "$EXEC_TIMEOUT_SEC" "$ps_out" "$ps_rc" docker ps --format '{{.Names}}'; then
    rc=$(cat "$ps_rc" 2>/dev/null || echo 1)
    rm -f "$ps_out" "$ps_rc"
    echo "warning: docker ps failed/timeout rc=${rc}" >&2
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

  echo "found ${#containers[@]} containers" >&2

  if (( ${#containers[@]} == 0 )); then
    echo "no running containers matching ^${NAME_PREFIX}[0-9]*$" >&2
    sleep "$INTERVAL_SEC"
    continue
  fi

  for c in "${containers[@]}"; do
    echo "[$c] fast-checking processcache..." >&2

    if ! docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -q '^true$'; then
      echo "[$c] skipped (not running)" >&2
      continue
    fi

    match_found=0
    fast_out=$(mktemp)
    fast_rc=$(mktemp)
    if docker_exec_timeout "$c" "$EXEC_TIMEOUT_SEC" "$fast_out" "$fast_rc" bash -lc "tetra dump processcache --exclude-execve-map-processes --skip-zero-refcnt --max-recv-size ${MAX_RECV_SIZE}"; then
      if grep -F -m1 -q "$REFCNT_GREP" "$fast_out"; then
        match_found=1
      fi
    else
      rc=$(cat "$fast_rc" 2>/dev/null || echo 1)
      echo "[$c] warning: fast check failed/timeout rc=${rc}" >&2
    fi
    rm -f "$fast_out" "$fast_rc"

    if (( match_found == 0 )); then
      echo "[$c] OK" >&2
      continue
    fi

    echo "[$c] refcnt pattern matched; validating refcnt_ops..." >&2

    tmp_cache=$(mktemp)
    dump_rc=$(mktemp)
    if ! docker_exec_timeout "$c" "$EXEC_TIMEOUT_SEC" "$tmp_cache" "$dump_rc" bash -lc "tetra dump processcache --exclude-execve-map-processes --skip-zero-refcnt --max-recv-size ${MAX_RECV_SIZE}"; then
      rm -f "$tmp_cache"
      rc=$(cat "$dump_rc" 2>/dev/null || echo 1)
      rm -f "$dump_rc"
      echo "[$c] warning: failed to dump processcache" >&2
      continue
    fi
    rm -f "$dump_rc"

    offenders_tmp=$(mktemp)
    offenders_count=$(jq -c 'select((.refcnt_ops["parent--"] // 0) > (.refcnt_ops["parent++"] // 0))' "$tmp_cache" \
      | tee "$offenders_tmp" \
      | wc -l \
      | tr -d ' ')
    if [[ -z "$offenders_count" ]]; then
      offenders_count=0
    fi

    if (( offenders_count > 0 )); then
      local_time=$(date)
      ts=$(date +'%Y%m%d_%H%M%S')
      found_dump="${HOST_OUT_DIR}/FOUND_${ts}_${c}_${offenders_count}.json"
      mv "$tmp_cache" "$found_dump"

      echo "LOCAL_TIME: ${local_time}"
      echo "[$c] FOUND offenders: ${offenders_count}"
      echo "[$c] cache saved: ${found_dump}"
    #   cat "$offenders_tmp"
      rm -f "$offenders_tmp"
      continue
    #   exit 1
    fi

    rm -f "$tmp_cache" "$offenders_tmp"
    echo "[$c] OK" >&2
  done

  sleep "$INTERVAL_SEC"
done

