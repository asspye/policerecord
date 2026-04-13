#!/bin/sh
set -e

CONFIG=${CONFIG_PATH:-/opt/tv-recorder/config/channels.yaml}
API_PORT=${API_PORT:-5000}

echo "=========================================="
echo " TV Recorder"
echo " Config:   $CONFIG"
echo " API port: $API_PORT"
echo "=========================================="

# Запускаем рекордер в фоне
./recorder --config "$CONFIG" &
RECORDER_PID=$!

# Запускаем API-сервер в фоне
./api-server --config "$CONFIG" --port "$API_PORT" &
API_PID=$!

# Ждём любого из процессов — если один упал, останавливаем контейнер
wait -n $RECORDER_PID $API_PID
EXIT_CODE=$?

echo "Один из процессов завершился (код $EXIT_CODE), останавливаю контейнер..."
kill $RECORDER_PID $API_PID 2>/dev/null || true
exit $EXIT_CODE
