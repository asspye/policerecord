package main

import (
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	Version   = "1.0.0"
	BuildDate = "2024-12-04"
)

func main() {
	// Флаги командной строки
	configPath := flag.String("config", "config/channels.yaml", "Путь к конфигурационному файлу")
	testMode := flag.Bool("test", false, "Тестовый режим (не запускать запись)")
	version := flag.Bool("version", false, "Показать версию")
	flag.Parse()

	// Показать версию
	if *version {
		log.Printf("TV Recorder Backend v%s (build %s)", Version, BuildDate)
		os.Exit(0)
	}

	// Создать директории до открытия лог-файла
	checkDirectories()

	// Создать логгер — писать и в файл, и в консоль одновременно
	logFile, err := os.OpenFile("logs/tv-recorder.log",
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("❌ Ошибка создания лог-файла: %v", err)
	}
	defer logFile.Close()

	logger := log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)

	// Красивый баннер
	printBanner(logger)

	// Проверить FFmpeg
	if !checkFFmpeg(logger) {
		logger.Fatal("❌ FFmpeg не установлен. Установите: sudo apt-get install ffmpeg")
	}

	// Загрузить конфигурацию
	logger.Printf("📂 Загрузка конфигурации: %s", *configPath)
	configManager := NewConfigManager(*configPath)
	
	if err := configManager.Load(); err != nil {
		logger.Fatalf("❌ Ошибка загрузки конфигурации: %v", err)
	}

	channels := configManager.GetChannels()
	logger.Printf("📋 Загружено каналов: %d", len(channels))
	
	// Показать информацию о каналах
	for _, ch := range channels {
		status := "❌ отключен"
		if ch.Enabled {
			status = "✅ активен"
		}
		logger.Printf("   - %s: %s:%d (%s) [%s]", 
			ch.Name, ch.MulticastIP, ch.Port, ch.Interface, status)
	}

	// Показать параметры записи
	rec := configManager.Config.Recording
	logger.Println("\n⚙️  Параметры записи:")
	logger.Printf("   - Разрешение: %s", rec.Resolution)
	logger.Printf("   - Битрейт видео: %s", rec.Bitrate)
	logger.Printf("   - FPS: %s", rec.FPS)
	logger.Printf("   - Кодек: %s", rec.Codec)
	logger.Printf("   - Битрейт аудио: %s", rec.AudioBitrate)
	logger.Printf("   - Длительность сегмента: %d сек (%d мин)", 
		rec.SegmentDuration, rec.SegmentDuration/60)

	logger.Println("📁 Директории готовы")

	// Тестовый режим
	if *testMode {
		logger.Println("\n🧪 ТЕСТОВЫЙ РЕЖИМ - запись не будет запущена")
		logger.Println("✅ Конфигурация корректна, можно запускать")
		return
	}

	// Создать рекордер
	recorder := NewRecorder(configManager, logger)

	// Канал для сигналов завершения
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Запустить запись в отдельной горутине
	go recorder.Start()

	logger.Println("\n✅ Система запущена")
	logger.Println("💡 Нажмите Ctrl+C для остановки...")
	logger.Println(strings.Repeat("=", 60))

	// Периодически выводить статус
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			status := recorder.GetStatus()
			if status["running"].(bool) && status["active_count"].(int) > 0 {
				logger.Printf("📊 Статус: записывается %d каналов", 
					status["active_count"].(int))
			}
		}
	}()

	// Ожидание сигнала завершения
	sig := <-sigChan
	logger.Printf("\n⚠️  Получен сигнал: %v", sig)

	// Остановка рекордера
	logger.Println("🛑 Останавливаю запись...")
	recorder.Stop()

	// Подождать немного для корректного завершения
	time.Sleep(2 * time.Second)

	logger.Println("👋 TV Recorder остановлен")
	logger.Println(strings.Repeat("=", 60))
}

// printBanner - красивый баннер при запуске
func printBanner(logger *log.Logger) {
	banner := `
╔══════════════════════════════════════════════════════════════╗
║                                                              ║
║          📺  TV Recorder Backend (Go)                       ║
║                                                              ║
║          Система записи UDP мультикаст потоков             ║
║          Version: ` + Version + `                            ║
║                                                              ║
╚══════════════════════════════════════════════════════════════╝
`
	logger.Println(banner)
	logger.Printf("⏰ Запуск: %s", time.Now().Format("02.01.2006 15:04:05 MSK"))
	logger.Println(strings.Repeat("=", 60))
}

// checkFFmpeg - проверить наличие FFmpeg
func checkFFmpeg(logger *log.Logger) bool {
	cmd := exec.Command("ffmpeg", "-version")
	if err := cmd.Run(); err != nil {
		return false
	}
	logger.Println("✅ FFmpeg найден")
	return true
}

// checkDirectories - проверить и создать необходимые директории
func checkDirectories() {
	dirs := []string{
		"logs",
		"config",
		"/opt/tv-recorder/recordings",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("❌ Не удалось создать директорию %s: %v", dir, err)
		}
	}
}
