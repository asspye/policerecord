# ── Stage 1: сборка бинарников ────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Зависимости
COPY go.mod go.sum ./
RUN go mod download

# Исходники
COPY *.go ./

# Собираем оба бинарника
RUN go build -o recorder   main.go      recorder.go config.go && \
    go build -o api-server api-server.go webserver.go config.go

# ── Stage 2: финальный образ ──────────────────────────────────────────────────
FROM debian:bookworm-slim

# FFmpeg + шрифты для drawtext
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg \
        fonts-dejavu-core \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/tv-recorder

# Бинарники из builder
COPY --from=builder /build/recorder    ./
COPY --from=builder /build/api-server  ./

# Фронтенд
COPY frontend.html ./

# Создать нужные директории
RUN mkdir -p \
    /opt/tv-recorder/recordings \
    /opt/tv-recorder/config \
    /opt/tv-recorder/logs

# Дефолтный конфиг (перекрывается volume-монтированием при деплое)
COPY config/ ./config/

COPY entrypoint.sh ./
RUN chmod +x entrypoint.sh

EXPOSE 5000

ENTRYPOINT ["./entrypoint.sh"]
