#!/bin/bash
# Установка очистки записей на сервер записи (police1). Запуск: sudo bash install.sh
# Первый запуск сразу удаляет всё старше 40 дней.
set -e
cd "$(dirname "$0")"
install -m 0755 policerecord-cleanup /usr/local/sbin/policerecord-cleanup
install -m 0644 policerecord-cleanup.service policerecord-cleanup.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now policerecord-cleanup.timer
systemctl start policerecord-cleanup.service
journalctl -u policerecord-cleanup.service -n 3 --no-pager -o cat
systemctl list-timers policerecord-cleanup.timer --no-legend
df -h /mnt/recordings
