#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PID_FILE="$ROOT_DIR/.pansou-dev.pid"
LOG_FILE="$ROOT_DIR/.pansou-dev.log"
BIN_FILE="$ROOT_DIR/.pansou-dev-bin"
PORT="${PORT:-8888}"

source_signature() {
  (
    cd "$ROOT_DIR"
    find . -type f \( -name "*.go" -o -name "go.mod" -o -name "go.sum" \) \
      ! -path "./.git/*" \
      ! -path "./cache/*" \
      ! -path "./node_modules/*" \
      ! -path "./typescript/node_modules/*" \
      -print0 | xargs -0 stat -f "%m %N" 2>/dev/null | shasum | awk '{print $1}'
  )
}

is_pid_running() {
  local pid="$1"
  kill -0 "$pid" >/dev/null 2>&1
}

port_is_listening() {
  lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1
}

port_listener_pids() {
  lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null || true
}

kill_port_listeners() {
  local pids
  pids="$(port_listener_pids)"
  if [[ -z "$pids" ]]; then
    return 0
  fi

  echo "$pids" | xargs kill >/dev/null 2>&1 || true
  sleep 1

  pids="$(port_listener_pids)"
  if [[ -n "$pids" ]]; then
    echo "$pids" | xargs kill -9 >/dev/null 2>&1 || true
  fi
}

extract_compose_env() {
  local key="$1"
  local compose_file="$ROOT_DIR/docker-compose.yml"
  if [[ ! -f "$compose_file" ]]; then
    return 0
  fi

  grep -m1 "${key}=" "$compose_file" | sed "s/.*${key}=//"
}

load_default_env() {
  export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.24.9}"
  export PORT="$PORT"

  # 开发环境默认代理（可通过外部环境变量覆盖）
  export http_proxy="${http_proxy:-${HTTP_PROXY:-http://127.0.0.1:7897}}"
  export https_proxy="${https_proxy:-${HTTPS_PROXY:-http://127.0.0.1:7897}}"
  export all_proxy="${all_proxy:-${ALL_PROXY:-socks5://127.0.0.1:7897}}"
  export HTTP_PROXY="${HTTP_PROXY:-$http_proxy}"
  export HTTPS_PROXY="${HTTPS_PROXY:-$https_proxy}"
  export ALL_PROXY="${ALL_PROXY:-$all_proxy}"
  export no_proxy="${no_proxy:-${NO_PROXY:-127.0.0.1,localhost}}"
  export NO_PROXY="${NO_PROXY:-$no_proxy}"

  export CACHE_ENABLED="${CACHE_ENABLED:-true}"
  export CACHE_PATH="${CACHE_PATH:-./cache}"
  export CACHE_MAX_SIZE="${CACHE_MAX_SIZE:-100}"
  export CACHE_TTL="${CACHE_TTL:-60}"
  export ASYNC_PLUGIN_ENABLED="${ASYNC_PLUGIN_ENABLED:-true}"
  export ASYNC_RESPONSE_TIMEOUT="${ASYNC_RESPONSE_TIMEOUT:-4}"
  export ASYNC_MAX_BACKGROUND_WORKERS="${ASYNC_MAX_BACKGROUND_WORKERS:-20}"
  export ASYNC_MAX_BACKGROUND_TASKS="${ASYNC_MAX_BACKGROUND_TASKS:-100}"
  export ASYNC_CACHE_TTL_HOURS="${ASYNC_CACHE_TTL_HOURS:-1}"

  if [[ -z "${CHANNELS:-}" ]]; then
    CHANNELS="$(extract_compose_env CHANNELS)"
    export CHANNELS
  fi

  if [[ -z "${ENABLED_PLUGINS:-}" ]]; then
    ENABLED_PLUGINS="$(extract_compose_env ENABLED_PLUGINS)"
    export ENABLED_PLUGINS
  fi
}

start() {
  if [[ -f "$PID_FILE" ]]; then
    local pid
    pid="$(cat "$PID_FILE")"
    if [[ -n "$pid" ]] && is_pid_running "$pid"; then
      echo "pansou 已在运行 (PID: $pid)"
      exit 0
    fi
    rm -f "$PID_FILE"
  fi

  if port_is_listening; then
    echo "端口 $PORT 已被占用，请先释放端口后再启动。"
    lsof -nP -iTCP:"$PORT" -sTCP:LISTEN
    exit 1
  fi

  load_default_env

  if ! command -v go >/dev/null 2>&1; then
    echo "未找到 go 命令，请先安装 Go。"
    exit 1
  fi

  echo "启动中: 端口=$PORT, 日志=$LOG_FILE"
  echo "代理: http_proxy=$http_proxy https_proxy=$https_proxy all_proxy=$all_proxy"
  (
    cd "$ROOT_DIR"
    go build -o "$BIN_FILE" .
    nohup "$BIN_FILE" >"$LOG_FILE" 2>&1 &
    echo $! >"$PID_FILE"
  )

  local i
  for i in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
      echo "启动成功 (PID: $(cat "$PID_FILE"))"
      return 0
    fi
    sleep 1
  done

  echo "启动超时，请检查日志: $LOG_FILE"
  tail -n 40 "$LOG_FILE" || true
  exit 1
}

watch() {
  load_default_env

  if ! command -v go >/dev/null 2>&1; then
    echo "未找到 go 命令，请先安装 Go。"
    exit 1
  fi

  if port_is_listening; then
    if [[ -f "$PID_FILE" ]]; then
      stop
    else
      echo "端口 $PORT 已被其他进程占用，无法进入热重启模式。"
      lsof -nP -iTCP:"$PORT" -sTCP:LISTEN
      exit 1
    fi
  fi

  echo "热重启模式启动: 监听 Go 源码变化并自动重启"
  echo "日志文件: $LOG_FILE"
  echo "代理: http_proxy=$http_proxy https_proxy=$https_proxy all_proxy=$all_proxy"

  cleanup_watch() {
    if [[ -f "$PID_FILE" ]]; then
      local wpid
      wpid="$(cat "$PID_FILE")"
      if [[ -n "$wpid" ]] && is_pid_running "$wpid"; then
        kill "$wpid" >/dev/null 2>&1 || true
      fi
      rm -f "$PID_FILE"
    fi
    echo "热重启模式已退出"
  }

  trap cleanup_watch INT TERM

  local last_sig=""
  while true; do
    local cur_sig
    cur_sig="$(source_signature)"

    if [[ "$cur_sig" != "$last_sig" ]]; then
      last_sig="$cur_sig"
      echo "检测到源码变更，重新构建并重启..."

      if [[ -f "$PID_FILE" ]]; then
        local old_pid
        old_pid="$(cat "$PID_FILE")"
        if [[ -n "$old_pid" ]] && is_pid_running "$old_pid"; then
          kill "$old_pid" >/dev/null 2>&1 || true
          sleep 1
        fi
        rm -f "$PID_FILE"
      fi

      (
        cd "$ROOT_DIR"
        if ! go build -o "$BIN_FILE" .; then
          echo "构建失败，等待下一次文件变更重试。"
          exit 0
        fi
        nohup "$BIN_FILE" >"$LOG_FILE" 2>&1 &
        echo $! >"$PID_FILE"
      )

      sleep 1
      if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
        echo "重启成功 (PID: $(cat "$PID_FILE"))"
      else
        echo "重启后健康检查未通过，请查看日志: $LOG_FILE"
        tail -n 20 "$LOG_FILE" || true
      fi
    fi

    sleep 1
  done
}

stop() {
  if [[ ! -f "$PID_FILE" ]]; then
    echo "未发现受脚本管理的运行进程。"
    if port_is_listening; then
      local pids
      pids="$(port_listener_pids)"
      echo "提示: 端口 $PORT 仍有监听进程，尝试按进程名清理..."
      for p in $pids; do
        local comm
        comm="$(ps -p "$p" -o comm= 2>/dev/null | xargs || true)"
        if [[ "$comm" == "pansou" ]] || [[ "$comm" == ".pansou-dev-bin" ]]; then
          kill "$p" >/dev/null 2>&1 || true
        fi
      done
      sleep 1
      if port_is_listening; then
        lsof -nP -iTCP:"$PORT" -sTCP:LISTEN
      fi
    fi
    exit 0
  fi

  local pid
  pid="$(cat "$PID_FILE")"

  if [[ -z "$pid" ]] || ! is_pid_running "$pid"; then
    rm -f "$PID_FILE"
    if port_is_listening; then
      echo "PID 文件已过期，尝试清理端口 $PORT 的监听进程..."
      kill_port_listeners
    fi
    if port_is_listening; then
      echo "停止失败: 端口 $PORT 仍被占用。"
      lsof -nP -iTCP:"$PORT" -sTCP:LISTEN
      exit 1
    fi
    echo "PID 文件已过期，已清理。"
    exit 0
  fi

  kill "$pid"
  local i
  for i in $(seq 1 10); do
    if ! is_pid_running "$pid"; then
      rm -f "$PID_FILE"
      echo "已停止 (PID: $pid)"
      return 0
    fi
    sleep 1
  done

  echo "进程未在预期时间退出，执行强制停止..."
  kill -9 "$pid" >/dev/null 2>&1 || true
  rm -f "$PID_FILE"
  if port_is_listening; then
    kill_port_listeners
  fi

  if port_is_listening; then
    echo "停止失败: 端口 $PORT 仍被占用。"
    lsof -nP -iTCP:"$PORT" -sTCP:LISTEN
    exit 1
  fi

  echo "已强制停止 (PID: $pid)"
}

status() {
  local managed_pid=""

  if [[ -f "$PID_FILE" ]]; then
    managed_pid="$(cat "$PID_FILE")"
  fi

  if [[ -n "$managed_pid" ]] && is_pid_running "$managed_pid"; then
    echo "状态: 运行中 (PID: $managed_pid)"
  else
    echo "状态: 未运行(或非脚本托管)"
  fi

  if port_is_listening; then
    echo "端口: $PORT 正在监听"
  else
    echo "端口: $PORT 未监听"
  fi

  if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
    echo "健康检查: 正常"
  else
    echo "健康检查: 失败"
  fi

  if [[ -f "$LOG_FILE" ]]; then
    echo "日志文件: $LOG_FILE"
  fi
}

case "${1:-}" in
  start)
    start
    ;;
  stop)
    stop
    ;;
  status)
    status
    ;;
  watch)
    watch
    ;;
  restart)
    stop
    start
    ;;
  *)
    echo "用法: $0 {start|stop|status|watch|restart}"
    exit 1
    ;;
esac