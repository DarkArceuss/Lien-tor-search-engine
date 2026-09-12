#!/data/data/com.termux/files/usr/bin/bash

set -u

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ONION_HOSTER_DIR="$ROOT/onion-hoster"
ONION_HOSTER="$ONION_HOSTER_DIR/termux.sh"
BASH_BIN="${PREFIX:-}/bin/bash"
if [ ! -x "$BASH_BIN" ]; then
    BASH_BIN="$(command -v bash 2>/dev/null || true)"
fi
PORT="${LIEN_PORT:-3000}"
PID_FILE="$ROOT/.lien.pid"
LOG_FILE="$ROOT/.lien.log"
RUNTIME_DIR="${PREFIX:-$HOME/.termux}/tmp/lien-startup"
BINARY="$RUNTIME_DIR/lien-server"
ONION_ADDRESS_FILE="$ROOT/.onion-address"

command_exists() {
    command -v "$1" >/dev/null 2>&1
}


run_onion_hoster() {
    if [ ! -f "$ONION_HOSTER" ]; then
        printf '%s\n' "Onion Hoster script was not found: $ONION_HOSTER" >&2
        return 1
    fi
    if [ -z "$BASH_BIN" ]; then
        printf '%s\n' "Bash is required to run Onion Hoster." >&2
        return 1
    fi
    "$BASH_BIN" "$ONION_HOSTER" "$@"
}

pid_is_lien() {
    local pid="$1"
    case "$pid" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ -r "/proc/$pid/cmdline" ] || return 1
    tr '\000' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -F -- "$BINARY" >/dev/null 2>&1
}

read_pid_file() {
    local pid=
    [ -f "$PID_FILE" ] || return 1
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    case "$pid" in
        ''|*[!0-9]*) return 1 ;;
    esac
    printf '%s\n' "$pid"
}

server_running() {
    curl -fsS --max-time 2 "http://127.0.0.1:$PORT/health" >/dev/null 2>&1
}

lien_process_running() {
    local pid=
    pid="$(read_pid_file || true)"
    [ -n "$pid" ] || return 1
    kill -0 "$pid" 2>/dev/null || return 1
    pid_is_lien "$pid"
}

port_in_use() {
    if command_exists ss; then
        ss -ltn 2>/dev/null | awk -v port=":$PORT" '$4 ~ port"$" {found=1} END {exit(found ? 0 : 1)}'
        return $?
    fi
    if command_exists netstat; then
        netstat -ltn 2>/dev/null | awk -v port=":$PORT" '$4 ~ port"$" {found=1} END {exit(found ? 0 : 1)}'
        return $?
    fi
    (exec 3<>"/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1
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

kill_lien_pid() {
    local pid="$1"
    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        for _ in 1 2 3 4 5; do
            kill -0 "$pid" 2>/dev/null || return 0
            sleep 1
        done
        kill -9 "$pid" 2>/dev/null || true
    fi
}

cleanup_stale_lien() {
    local pid
    pid="$(read_pid_file || true)"
    if [ -n "$pid" ] && pid_is_lien "$pid"; then
        kill_lien_pid "$pid"
    fi

    while read -r pid; do
        [ -n "$pid" ] || continue
        kill_lien_pid "$pid"
    done < <(find_lien_pids)

    rm -f "$PID_FILE"
}

read_onion_address() {
    if [ -f "$ONION_ADDRESS_FILE" ]; then
        cat "$ONION_ADDRESS_FILE"
        return 0
    fi

    if [ -f "$HOME/.tor/hidden_service/hostname" ]; then
        cat "$HOME/.tor/hidden_service/hostname"
        return 0
    fi

    return 1
}

clear_screen() {
    if [ -n "${TERM:-}" ] && command -v clear >/dev/null 2>&1; then
        clear 2>/dev/null
    else
        printf '[2J[H'
    fi
}

show_menu() {
    clear_screen
    printf '%s\n' '=== Lien Search Engine ==='
    printf '%s\n' '1. Activate server'
    printf '%s\n' '2. Stop server'
    printf '%s\n' '3. Restart server'
    printf '%s\n' '4. Server status'
    printf '%s\n' '0. Exit'
    printf '%s\n' '======================='
    printf 'Select option: '
}

progress_bar() {
    local current="$1"
    local total="$2"
    local width=32
    local filled=$((current * width / total))
    local empty=$((width - filled))
    local bar=""

    if [ "$filled" -gt 0 ]; then
        bar="$(printf '%*s' "$filled" '' | tr ' ' '#')"
    fi
    if [ "$empty" -gt 0 ]; then
        bar="$bar$(printf '%*s' "$empty" '' | tr ' ' '-')"
    fi

    printf '\r[%s] %3d%%' "$bar" "$((current * 100 / total))"
}

ensure_dependencies() {
    printf '%s\n' 'Checking dependencies...'

    if ! command_exists go; then
        printf '%s\n' 'Go is not installed. Installing...'
        pkg install golang -y || return 1
    fi

    if ! command_exists curl; then
        printf '%s\n' 'curl is not installed. Installing...'
        pkg install curl -y || return 1
    fi

    if ! command_exists tor; then
        printf '%s\n' 'Tor is not installed. Installing...'
        if ! pkg install tor -y; then
            return 1
        fi
    fi

    return 0
}

build_server() {
    mkdir -p "$RUNTIME_DIR" || return 1

    if [ -x "$BINARY" ] && [ "$BINARY" -nt "$ROOT/scripts/main.go" ] && [ "$BINARY" -nt "$ROOT/index.html" ]; then
        return 0
    fi

    printf '%s\n' 'Building Lien server...'
    (cd "$ROOT" && go build -o "$BINARY" ./scripts)
}

start_lien_process() {
    if server_running; then
        return 0
    fi

    cleanup_stale_lien

    if port_in_use; then
        printf 'Port %s is already in use by another process.\n' "$PORT" >&2
        printf '%s\n' 'Stop that process or set LIEN_PORT to another free port.' >&2
        return 1
    fi

    : > "$LOG_FILE"
    cd "$ROOT" || return 1
    nohup env LIEN_BIND="127.0.0.1:$PORT" "$BINARY" >"$LOG_FILE" 2>&1 &
    local pid=$!
    printf '%s\n' "$pid" > "$PID_FILE"

    local attempt=1
    while [ "$attempt" -le 10 ]; do
        if server_running; then
            return 0
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            break
        fi
        sleep 1
        attempt=$((attempt + 1))
    done

    printf '%s\n' 'Lien server failed to start.' >&2
    [ -f "$LOG_FILE" ] && cat "$LOG_FILE" >&2
    cleanup_stale_lien
    return 1
}

wait_for_http() {
    local attempt=1
    while [ "$attempt" -le 20 ]; do
        if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        attempt=$((attempt + 1))
    done
    return 1
}

configure_onion_for_lien() {
    run_onion_hoster method custom_port "$PORT" || return 1
    return 0
}

start_onion() {
    configure_onion_for_lien || return 1
    run_onion_hoster start || return 1

    local address=
    local attempt=1
    while [ "$attempt" -le 20 ]; do
        address="$(run_onion_hoster address 2>/dev/null | tail -n 1 | tr -d "\r")" || address=
        case "$address" in
            *.onion)
                printf '%s\n' "$address" > "$ONION_ADDRESS_FILE"
                return 0
                ;;
        esac
        sleep 1
        attempt=$((attempt + 1))
    done
    return 1
}

activate_server() {
    clear_screen
    printf '%s\n' 'Starting Lien Search Engine...'
    printf '%s\n' ''

    if [ ! -f "$ONION_HOSTER" ]; then
        printf '%s\n' "Onion Hoster script was not found: $ONION_HOSTER" >&2
        printf '%s\n' "Press enter for exit..."
        read -r
        return
    fi

    printf '%s\n' 'Checking and installing required packages...'
    if ! ensure_dependencies; then
        printf '%s\n' 'Press enter for exit...'
        read -r
        return
    fi
    progress_bar 1 4
    printf '\n'

    if ! build_server; then
        printf '%s\n' 'Press enter for exit...'
        read -r
        return
    fi
    progress_bar 2 4
    printf '\n'

    printf '%s\n' 'Launching Lien server...'
    if ! start_lien_process || ! wait_for_http; then
        printf '%s\n' 'Press enter for exit...'
        read -r
        return
    fi
    progress_bar 3 4
    printf '\n'

    printf '%s\n' 'Starting Onion service...'
    if ! start_onion; then
        printf '\n%s\n' 'Onion service failed to start.'
        stop_lien_only
        printf '%s\n' 'Press enter for exit...'
        read -r
        return
    fi

    local onion_address
    onion_address="$(cat "$ONION_ADDRESS_FILE" 2>/dev/null || true)"
    if [ -z "$onion_address" ]; then
        printf '%s\n' 'Onion service started, but the hostname file was not created.' >&2
        stop_server_quiet
        stop_lien_only
        printf '%s\n' 'Press enter for exit...'
        read -r
        return
    fi
    printf '%s\n' "$onion_address" > "$ONION_ADDRESS_FILE"

    progress_bar 4 4
    printf '\n\n'
    printf 'Server started... Onion link:\n'
    printf '%s\n' "$onion_address"
    read -r
}

stop_server_quiet() {
    if [ -f "$ONION_HOSTER" ]; then
        run_onion_hoster stop >/dev/null 2>&1 || true
    fi
}

stop_lien_only() {
    local pid
    pid="$(read_pid_file || true)"
    if [ -n "$pid" ] && pid_is_lien "$pid" && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        for _ in 1 2 3 4 5; do
            kill -0 "$pid" 2>/dev/null || break
            sleep 1
        done
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
        fi
    fi
    rm -f "$PID_FILE"
}

stop_server() {
    clear_screen
    printf '%s\n' 'Stopping server...'

    if [ -f "$ONION_HOSTER" ]; then
        run_onion_hoster stop || true
    fi

    stop_lien_only
    printf '%s\n' 'Server stopped.'
    printf '\nPress enter for exit...\n'
    read -r
}

restart_server() {
    clear_screen
    printf '%s\n' 'Restarting server...'
    printf '%s\n' ''

    if [ -f "$ONION_HOSTER" ]; then
        run_onion_hoster stop || true
    fi
    stop_lien_only
    sleep 1

    activate_server
}

status_server() {
    clear_screen
    local status='not work'
    local onion_address

    if server_running; then
        status='work'
    fi

    onion_address="$(read_onion_address || true)"

    printf 'Server status: %s\n' "$status"
    printf 'Onion link: %s\n' "${onion_address:-Unavailable}"
    printf '\nPress enter for exit...\n'
    read -r
}

trap 'stop_lien_only' INT TERM

while true; do
    show_menu
    read -r option
    case "$option" in
        1) activate_server ;;
        2) stop_server ;;
        3) restart_server ;;
        4) status_server ;;
        0) exit 0 ;;
        *) : ;;
    esac
done
