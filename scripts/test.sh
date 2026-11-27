#!/usr/bin/env bash
set -euo pipefail

redis_port=""
redis_pidfile=""

cleanup_redis() {
	if [[ -n "$redis_port" ]]; then
		redis-cli -h 127.0.0.1 -p "$redis_port" shutdown nosave >/dev/null 2>&1 || true
	fi
	if [[ -n "$redis_pidfile" ]]; then
		rm -f "$redis_pidfile"
	fi
}

if [[ -z "${TEST_REDIS_URL:-}" ]]; then
	if ! command -v redis-server >/dev/null 2>&1 || ! command -v redis-cli >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
		echo "Set TEST_REDIS_URL or install redis-server, redis-cli, and python3 to run Redis-backed tests." >&2
		exit 1
	fi
	redis_port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
	redis_pidfile="$(mktemp "${TMPDIR:-/tmp}/goreportcard-redis.XXXXXX")"
	rm -f "$redis_pidfile"
	trap cleanup_redis EXIT
	redis-server --bind 127.0.0.1 --port "$redis_port" --save "" --appendonly no --daemonize yes --pidfile "$redis_pidfile" --logfile /dev/null
	export TEST_REDIS_URL="redis://127.0.0.1:${redis_port}/15"
	for _ in {1..50}; do
		if redis-cli -h 127.0.0.1 -p "$redis_port" ping >/dev/null 2>&1; then
			break
		fi
		sleep 0.1
	done
	if ! redis-cli -h 127.0.0.1 -p "$redis_port" ping >/dev/null 2>&1; then
		echo "Temporary Redis server did not become ready." >&2
		exit 1
	fi
fi

go test -coverprofile=coverage.txt ./src/...
coverage="$(go tool cover -func=coverage.txt | awk '/^total:/ { gsub("%", "", $3); print $3 }')"
echo "Total statement coverage: ${coverage}%"
awk -v coverage="$coverage" 'BEGIN { if (coverage <= 98) { print "Coverage must be above 98%"; exit 1 } }'
