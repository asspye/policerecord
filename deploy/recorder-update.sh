#!/usr/bin/env bash
# Обновление recorder.go на сервере записи и пересборка tv-recorder.
# Запуск с Mac (из каталога policerecord):
#   scp -J asspye@live-2.otcnet.ru:60023 recorder.go asspye@192.168.1.59:/tmp/recorder.go
#   ssh -J asspye@live-2.otcnet.ru:60023 asspye@192.168.1.59 'sudo bash -s' < deploy/recorder-update.sh
# Откат: docker tag policerecord-tv-recorder:pre-update policerecord-tv-recorder:latest
#        && cp recorder.go.bak-<время> recorder.go && docker compose up -d --no-build tv-recorder
set -euo pipefail

cd /opt/policerecord
ts=$(date +%Y%m%d-%H%M)

cp recorder.go "recorder.go.bak-$ts"
docker tag policerecord-tv-recorder:latest policerecord-tv-recorder:pre-update
install -m 644 /tmp/recorder.go recorder.go

docker compose build tv-recorder
# Перезапуск контейнера: все каналы прерываются на ~10–20 с
docker compose up -d tv-recorder

sleep 30
docker ps --filter name=tv-recorder --format '{{.Names}} {{.Status}}'
echo "ffmpeg: $(pgrep -c ffmpeg)"
tail -n 40 logs/tv-recorder.log | grep -vE 'Перечитывание|📊'
