# TV Recorder — система записи UDP мультикаст потоков

Система для записи IPTV/мультикаст каналов с веб-интерфейсом управления.  
Написана на Go, фронтенд — чистый HTML/JS, запись через FFmpeg.

## Возможности

- Запись неограниченного числа UDP мультикаст потоков одновременно
- Наложение даты/времени и названия канала на видео (через FFmpeg drawtext)
- Сегментная запись в `.ts` файлы (по 1 часу по умолчанию)
- Hot-reload конфигурации — изменения применяются без перезапуска
- Автоперезапуск FFmpeg при обрывах потока
- Веб-интерфейс: просмотр, скачивание, ZIP-архив за дату
- REST API для управления каналами
- Встроенный видеоплеер с календарём по датам

## Архитектура

Два независимых бинарника собираются из одного репозитория:

```
recorder     — запускает FFmpeg для каждого активного канала
api-server   — HTTP сервер с REST API и веб-интерфейсом
```

Оба процесса работают в одном Docker-контейнере и разделяют файл конфигурации.

```
config/channels.yaml  ←→  recorder  →  /opt/tv-recorder/recordings/
                      ←→  api-server →  :5000 (web + REST API)
```

## Стек

| Компонент | Технология |
|---|---|
| Бэкенд | Go 1.23 |
| Запись | FFmpeg |
| Конфиг | YAML |
| Фронтенд | HTML + Vanilla JS |
| Контейнер | Docker (debian:bookworm-slim) |

## Структура проекта

```
.
├── main.go             # Точка входа рекордера
├── recorder.go         # Логика запуска FFmpeg и управления горутинами
├── config.go           # ConfigManager (потокобезопасный YAML CRUD)
├── api-server.go       # Точка входа API-сервера
├── webserver.go        # HTTP handlers
├── frontend.html       # Веб-интерфейс (SPA)
├── go.mod
├── Dockerfile
├── docker-compose.yml
├── entrypoint.sh
└── config/
    └── channels.yaml   # Конфигурация каналов
```

## Конфигурация

Файл `config/channels.yaml`:

```yaml
channels:
  - id: channel_1
    name: 1HD-Music-HD_ID001
    multicast_ip: 232.232.8.15
    port: 3333
    interface: mcast0       # сетевой интерфейс для приёма мультикаста
    enabled: true

  - id: channel_2
    name: SomeChannel
    multicast_ip: 233.198.134.1
    port: 3333
    interface: mcast0
    enabled: false

recording:
  bitrate: 500k            # битрейт видео
  fps: "20"                # частота кадров
  codec: libx264           # видеокодек (libx264 или copy)
  preset: veryfast         # пресет кодека (только для libx264)
  segment_duration: 3600   # длина сегмента в секундах (3600 = 1 час)
  audio_bitrate: 96k       # битрейт аудио
  resolution: 640x480      # разрешение выходного видео
```

Каналы можно добавлять, редактировать и включать/отключать через веб-интерфейс — изменения сохраняются в `channels.yaml` автоматически.

## REST API

| Метод | Путь | Описание |
|---|---|---|
| GET | `/api/channels` | Список каналов |
| POST | `/api/channels` | Добавить канал |
| GET | `/api/channels/:id` | Получить канал |
| PUT | `/api/channels/:id` | Обновить канал |
| DELETE | `/api/channels/:id` | Удалить канал |
| GET | `/api/files/:channel` | Файлы канала |
| GET | `/api/stream/:ch/:file` | Стриминг видеофайла |
| GET | `/api/download/:ch/:file` | Скачать файл |
| GET | `/api/archive/:ch/:date` | Скачать ZIP за дату (формат: `2025-12-04`) |
| GET | `/api/interfaces` | Сетевые интерфейсы хоста |
| GET | `/api/status` | Статус системы |

## Деплой через Docker

### Требования

- Docker 20.10+
- Docker Compose v2
- Сетевой интерфейс для мультикаста (например `mcast0`) настроен на хосте
- Место на диске для записей

### Быстрый старт

```bash
# 1. Клонировать репозиторий
git clone <repo-url>
cd policerecord

# 2. Настроить каналы
nano config/channels.yaml

# 3. Собрать и запустить
docker compose up -d

# 4. Открыть веб-интерфейс
# http://<ip-сервера>:5000
```

### Просмотр логов

```bash
# Все логи
docker compose logs -f

# Только последние 100 строк
docker compose logs --tail=100
```

### Перезапуск после изменения конфига

Конфиг перечитывается автоматически каждые 30 секунд. Для немедленного применения:

```bash
docker compose restart
```

### Обновление до новой версии

```bash
git pull
docker compose up -d --build
```

### Остановка

```bash
docker compose down
```

### Переменные окружения

| Переменная | По умолчанию | Описание |
|---|---|---|
| `CONFIG_PATH` | `/opt/tv-recorder/config/channels.yaml` | Путь к конфигу |
| `API_PORT` | `5000` | Порт веб-интерфейса |

### Тома (volumes)

| Путь в контейнере | Назначение |
|---|---|
| `/opt/tv-recorder/config` | Конфигурация каналов |
| `/opt/tv-recorder/recordings` | Видеозаписи |
| `/opt/tv-recorder/logs` | Логи рекордера и FFmpeg |

По умолчанию записи хранятся в `/opt/tv-recorder/recordings` на хосте.  
Чтобы изменить путь — отредактируйте `docker-compose.yml`:

```yaml
volumes:
  - /your/custom/path:/opt/tv-recorder/recordings
```

### Важно: network_mode: host

В `docker-compose.yml` используется `network_mode: host`. Это обязательно:  
Docker NAT не пропускает мультикаст-трафик, контейнер должен видеть сетевые интерфейсы хоста напрямую.

## Сборка без Docker

Требования: Go 1.23+, FFmpeg, шрифт DejaVu Sans Bold.

```bash
# Установить зависимости
go mod download

# Собрать рекордер
go build -o recorder main.go recorder.go config.go

# Собрать API-сервер
go build -o api-server api-server.go webserver.go config.go

# Запустить рекордер
./recorder --config config/channels.yaml

# Запустить API-сервер (в отдельном терминале)
./api-server --config config/channels.yaml --port 5000
```

Флаги рекордера:

```
--config   путь к конфигу (по умолчанию: config/channels.yaml)
--test     тестовый режим: проверить конфиг без запуска записи
--version  показать версию
```

## Записи

Файлы сохраняются по пути:
```
/opt/tv-recorder/recordings/<название_канала>/<название>_YYYY-MM-DD_HH-MM-SS.ts
```

Пример:
```
/opt/tv-recorder/recordings/1HD-Music-HD_ID001/1HD-Music-HD_ID001_2025-12-04_14-00-00.ts
```

## Логи

| Файл | Содержимое |
|---|---|
| `logs/tv-recorder.log` | Основной лог рекордера |
| `logs/ffmpeg_<channel_id>.log` | Stderr FFmpeg для каждого канала |
