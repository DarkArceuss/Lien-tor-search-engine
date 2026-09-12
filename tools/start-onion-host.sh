#!/data/data/com.termux/files/usr/bin/bash
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
ONION_HOSTER_DIR="${ONION_HOSTER_DIR:-$ROOT/onion-hoster}"
ONION_HOSTER="$ONION_HOSTER_DIR/termux.sh"
PORT="${LIEN_PORT:-3000}"
PID_FILE="$ROOT/.lien.pid"
RUNTIME_DIR="${PREFIX:-$HOME/.termux}/tmp/lien-startup"
BINARY="$RUNTIME_DIR/lien-server"

if [ ! -f "$ONION_HOSTER" ]; then
    printf '%s\n' "Onion-Hoster was not found at: $ONION_HOSTER_DIR" >&2
    printf '%s\n' "Clone it first: git clone https://github.com/uzairdeveloper223/Onion-Hoster.git ~/Onion-Hoster" >&2
    exit 1
fi

cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
    printf '%s\n' "Go is required. Install it with: pkg install golang" >&2
    exit 1
fi

read_pid() {
    [ -f "$PID_FILE" ] || return 1
    local pid
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    case "$pid" in ''|*[!0-9]*) return 1 ;; esac
    printf '%s\n' "$pid"
}

pid_is_lien() {
    local pid="$1"
    [ -r "/proc/$pid/cmdline" ] || return 1
    tr '\000' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -F -- "$BINARY" >/dev/null 2>&1
}

find_lien_pids() {
    local proc pid command_line
    for proc in /proc/[0-9]*; do
        pid="${proc##*/}"
        [ "$pid" = "$$" ] && continue
        [ -r "$proc/cmdline" ] || continue
        command_line="$(tr '\000' ' ' < "$proc/cmdline" 2>/dev/null || true)"
        case "$command_line" in
            *"$BINARY"*) printf '%s\n' "$pid" ;;
        esac
    done
}

stop_old_lien() {
    local pid
    pid="$(read_pid || true)"
    if [ -n "$pid" ] && pid_is_lien "$pid"; then
        kill "$pid" 2>/dev/null || true
        sleep 1
        kill -9 "$pid" 2>/dev/null || true
    fi
    while read -r pid; do
        [ -n "$pid" ] || continue
        kill "$pid" 2>/dev/null || true
        sleep 1
        kill -9 "$pid" 2>/dev/null || true
    done < <(find_lien_pids)
    rm -f "$PID_FILE"
}

if read_pid >/dev/null 2>&1 && pid_is_lien "$(read_pid)" && kill -0 "$(read_pid)" 2>/dev/null && curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    printf '%s\n' "Lien is already running on port $PORT."
else
    stop_old_lien
    if (command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | awk -v p=":$PORT" '$4 ~ p"$" {found=1} END {exit found ? 0 : 1}'); then
        printf '%s\n' "Port $PORT is already in use by another process." >&2
        exit 1
    fi
    mkdir -p "$RUNTIME_DIR"
    go build -o "$BINARY" ./scripts
    LIEN_BIND="127.0.0.1:$PORT" nohup "$BINARY" >"$ROOT/.lien.log" 2>&1 &
    echo $! > "$PID_FILE"
    sleep 1
fi

if ! curl -fsS "http://127.0.0.1:$PORT/" >/dev/null; then
    printf '%s\n' "Lien failed to start. Check $ROOT/.lien.log" >&2
    exit 1
fi

cd "$ONION_HOSTER_DIR"
bash ./termux.sh install tor
bash ./termux.sh method custom_port "$PORT"
bash ./termux.sh start
bash ./termux.sh address
