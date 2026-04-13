package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Флаги
	configPath := flag.String("config", "config/channels.yaml", "Путь к конфигурации")
	port := flag.String("port", "5000", "Порт API сервера")
	flag.Parse()

	// Логгер
	logger := log.New(os.Stdout, "[API] ", log.LstdFlags)

	logger.Println("╔══════════════════════════════════════════════════════════════╗")
	logger.Println("║          📡 TV Recorder API Server                         ║")
	logger.Println("╚══════════════════════════════════════════════════════════════╝")

	// Загрузить конфигурацию
	logger.Printf("📂 Загрузка конфигурации: %s", *configPath)
	configManager := NewConfigManager(*configPath)

	if err := configManager.Load(); err != nil {
		logger.Fatalf("❌ Ошибка загрузки конфигурации: %v", err)
	}

	channels := configManager.GetChannels()
	logger.Printf("📋 Каналов в конфигурации: %d", len(channels))

	// Создать веб-сервер
	webServer := NewWebServer(configManager, logger, *port)

	// Канал для сигналов
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Запустить сервер в горутине
	go func() {
		if err := webServer.Start(); err != nil {
			logger.Printf("❌ Ошибка сервера: %v", err)
		}
	}()

	logger.Println("✅ API сервер готов к работе")
	logger.Println("💡 Доступные endpoints:")
	logger.Println("   GET    /api/channels          - Список каналов")
	logger.Println("   POST   /api/channels          - Добавить канал")
	logger.Println("   PUT    /api/channels/:id      - Изменить канал")
	logger.Println("   DELETE /api/channels/:id      - Удалить канал")
	logger.Println("   GET    /api/files/:channel    - Файлы канала")
	logger.Println("   GET    /api/download/:ch/:file - Скачать файл")
	logger.Println("   GET    /api/stream/:ch/:file  - Стримить видео")
	logger.Println("   GET    /api/status            - Статус системы")
	logger.Println("   GET    /api/interfaces        - Сетевые интерфейсы")
	logger.Println("")
	logger.Println("🌐 Примеры запросов:")
	logger.Printf("   curl http://localhost:%s/api/channels\n", *port)
	logger.Printf("   curl http://localhost:%s/api/status\n", *port)
	logger.Println("")
	logger.Println("💡 Нажмите Ctrl+C для остановки...")
	logger.Println("============================================================")

	// Ожидание сигнала
	sig := <-sigChan
	logger.Printf("\n⚠️  Получен сигнал: %v", sig)
	logger.Println("🛑 Остановка API сервера...")

	webServer.Stop()

	logger.Println("👋 API сервер остановлен")
}
