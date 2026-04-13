package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Recorder - структура рекордера
type Recorder struct {
	configManager *ConfigManager
	logger        *log.Logger
	processes     map[string]*exec.Cmd
	stopChans     map[string]chan bool
	mu            sync.RWMutex
	running       bool
}

// NewRecorder - создание нового рекордера
func NewRecorder(cm *ConfigManager, logger *log.Logger) *Recorder {
	return &Recorder{
		configManager: cm,
		logger:        logger,
		processes:     make(map[string]*exec.Cmd),
		stopChans:     make(map[string]chan bool),
		running:       false,
	}
}

// Start - запустить запись всех активных каналов с hot-reload
func (r *Recorder) Start() {
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()

	r.logger.Println("🚀 Запуск системы записи...")

	// Запустить каналы первый раз
	r.startChannels()

	// Hot-reload: перечитывать конфиг каждые 30 секунд
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// ВАЖНО: Блокируем главный поток чтобы горутина работала
	for {
		select {
		case <-ticker.C:
			if !r.running {
				return
			}
			r.reloadConfig()
		}
	}
}

// startChannels - запустить запись активных каналов
func (r *Recorder) startChannels() {
	channels := r.configManager.GetChannels()
	activeCount := 0

	for _, ch := range channels {
		if ch.Enabled {
			// Проверить, не запущен ли уже
			r.mu.RLock()
			_, exists := r.stopChans[ch.ID]
			r.mu.RUnlock()

			if !exists {
				stopChan := make(chan bool)
				r.mu.Lock()
				r.stopChans[ch.ID] = stopChan
				r.mu.Unlock()

				go r.recordChannel(ch, stopChan)
				activeCount++
				r.logger.Printf("✅ Запущена запись: %s (%s:%d на %s)",
					ch.Name, ch.MulticastIP, ch.Port, ch.Interface)
			}
		}
	}

	if activeCount == 0 {
		r.logger.Println("⚠️  Нет активных каналов для записи")
		r.logger.Println("💡 Включите каналы в config/channels.yaml (enabled: true)")
	} else {
		r.logger.Printf("📹 Активно записывается: %d каналов", activeCount)
	}
}

// reloadConfig - перечитать конфиг и применить изменения
func (r *Recorder) reloadConfig() {
	r.logger.Println("🔄 Перечитывание конфигурации...")

	// Перезагрузить конфиг
	if err := r.configManager.Load(); err != nil {
		r.logger.Printf("❌ Ошибка загрузки конфига: %v", err)
		return
	}

	channels := r.configManager.GetChannels()
	activeChannels := make(map[string]Channel)

	// Создать карту активных каналов
	for _, ch := range channels {
		if ch.Enabled {
			activeChannels[ch.ID] = ch
		}
	}

	// Остановить каналы которые были отключены
	r.mu.RLock()
	currentChannels := make(map[string]chan bool)
	for id, stopChan := range r.stopChans {
		currentChannels[id] = stopChan
	}
	r.mu.RUnlock()

	for id, stopChan := range currentChannels {
		if _, shouldRun := activeChannels[id]; !shouldRun {
			r.logger.Printf("⏸️  Остановка канала: %s", id)
			select {
			case stopChan <- true:
			default:
			}
			close(stopChan)

			r.mu.Lock()
			delete(r.stopChans, id)
			r.mu.Unlock()
		}
	}

	// Запустить новые каналы
	for id, ch := range activeChannels {
		r.mu.RLock()
		_, exists := r.stopChans[id]
		r.mu.RUnlock()

		if !exists {
			stopChan := make(chan bool)
			r.mu.Lock()
			r.stopChans[id] = stopChan
			r.mu.Unlock()

			go r.recordChannel(ch, stopChan)
			r.logger.Printf("▶️  Запущен новый канал: %s", ch.Name)
		}
	}
}

// recordChannel - запись одного канала (работает в отдельной горутине)
func (r *Recorder) recordChannel(ch Channel, stopChan chan bool) {
	retryCount := 0
	maxRetries := 5
	retryDelay := 5 * time.Second

	for {
		select {
		case <-stopChan:
			r.logger.Printf("🛑 Остановка записи: %s", ch.Name)
			return
		default:
		}

		// Создать директорию для канала
		outputDir := r.getOutputDir(ch.Name)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			r.logger.Printf("❌ Ошибка создания директории %s: %v", outputDir, err)
			time.Sleep(retryDelay)
			continue
		}

		// Построить команду FFmpeg
		cmd := r.buildFFmpegCommand(ch, outputDir)

		r.mu.Lock()
		r.processes[ch.ID] = cmd
		r.mu.Unlock()

		r.logger.Printf("▶️  [%s] Запуск FFmpeg...", ch.Name)

		// Запустить процесс
		err := cmd.Run()

		r.mu.Lock()
		delete(r.processes, ch.ID)
		r.mu.Unlock()

		if err != nil {
			retryCount++
			r.logger.Printf("⚠️  [%s] Ошибка FFmpeg: %v (попытка %d/%d)",
				ch.Name, err, retryCount, maxRetries)

			if retryCount >= maxRetries {
				r.logger.Printf("❌ [%s] Превышено количество попыток. Канал остановлен.", ch.Name)
				return
			}
		} else {
			retryCount = 0 // Сбросить счётчик при успешной работе
		}

		// Проверить, надо ли перезапустить
		select {
		case <-stopChan:
			r.logger.Printf("🛑 Остановка записи: %s", ch.Name)
			return
		default:
		}

		r.logger.Printf("🔄 [%s] Перезапуск через %v...", ch.Name, retryDelay)
		time.Sleep(retryDelay)
	}
}

// buildFFmpegCommand - построить команду FFmpeg
func (r *Recorder) buildFFmpegCommand(ch Channel, outputDir string) *exec.Cmd {
	rec := r.configManager.Config.Recording

	// Входной URL (мультикаст) с указанием локального адреса интерфейса
	inputURL := fmt.Sprintf("udp://%s:%d?localaddr=%s&fifo_size=50000000&overrun_nonfatal=1&timeout=5000000",
		ch.MulticastIP, ch.Port, r.getInterfaceIP(ch.Interface))

	// Фильтр для наложения даты/времени - РАБОЧАЯ ВЕРСИЯ
	drawtext := "drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:" +
		"text='%{localtime}': " +
		"fontsize=28:fontcolor=white:x=10:y=10:" +
		"box=1:boxcolor=black@0.7:boxborderw=5"

	// Добавить название канала
	channelName := sanitizeName(ch.Name)
	drawtext += ",drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:" +
		fmt.Sprintf("text='%s':", channelName) +
		"fontsize=24:fontcolor=yellow:x=10:y=50:" +
		"box=1:boxcolor=black@0.7:boxborderw=5"

	// Шаблон для имени файла
	outputPattern := filepath.Join(outputDir, fmt.Sprintf("%s_%%Y-%%m-%%d_%%H-%%M-%%S.ts", channelName))

	// Команда FFmpeg - упрощенная и стабильная версия
        // Команда FFmpeg с ожиданием ключевого кадра
	// Команда FFmpeg - простая версия без экспериментальных параметров
        // Команда FFmpeg с поддержкой preset для H.264
        args := []string{
  	"-fflags", "+genpts+discardcorrupt",
	"-err_detect", "ignore_err",
	"-i", inputURL,
	"-vf", drawtext,
	"-c:v", rec.Codec,
	"-b:v", rec.Bitrate,
	"-s", rec.Resolution,
	"-r", rec.FPS,
}

       // Добавить preset если это H.264
        if rec.Codec == "libx264" && rec.Preset != "" {
	args = append(args, "-preset", rec.Preset)
}

        // Продолжить с остальными параметрами
        args = append(args,
	"-c:a", "copy",
	"-f", "segment",
	"-segment_time", fmt.Sprintf("%d", rec.SegmentDuration),
	"-segment_format", "mpegts",
	"-strftime", "1",
	"-reset_timestamps", "1",
	"-y",
	outputPattern,
)

cmd := exec.Command("ffmpeg", args...)
	// Логировать stderr FFmpeg
	logFile := filepath.Join("logs", fmt.Sprintf("ffmpeg_%s.log", ch.ID))
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		cmd.Stderr = f
	}

	return cmd
}

// getOutputDir - получить путь для записи
func (r *Recorder) getOutputDir(channelName string) string {
	safeName := sanitizeName(channelName)
	return filepath.Join("/opt/tv-recorder/recordings", safeName)
}

// getInterfaceIP - получить IP адрес сетевого интерфейса
func (r *Recorder) getInterfaceIP(ifaceName string) string {
	cmd := exec.Command("ip", "-4", "addr", "show", ifaceName)
	output, err := cmd.Output()
	if err != nil {
		r.logger.Printf("⚠️  Не удалось получить IP для %s: %v", ifaceName, err)
		return "0.0.0.0"
	}

	// Парсим вывод ip addr
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "inet ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// Убрать маску подсети (/30)
				ip := strings.Split(parts[1], "/")[0]
				return ip
			}
		}
	}

	r.logger.Printf("⚠️  IP не найден для %s, использую 0.0.0.0", ifaceName)
	return "0.0.0.0"
}

// Stop - остановить все записи
func (r *Recorder) Stop() {
	r.logger.Println("🛑 Остановка системы записи...")

	r.mu.Lock()
	r.running = false
	stopChans := r.stopChans
	r.stopChans = make(map[string]chan bool)
	r.mu.Unlock()

	// Отправить сигнал остановки всем каналам
	for _, stopChan := range stopChans {
		select {
		case stopChan <- true:
		default:
		}
		close(stopChan)
	}

	// Принудительно убить процессы FFmpeg
	r.mu.Lock()
	for id, cmd := range r.processes {
		if cmd.Process != nil {
			r.logger.Printf("⚠️  Убиваю процесс для канала: %s", id)
			cmd.Process.Kill()
		}
	}
	r.mu.Unlock()

	r.logger.Println("✅ Все записи остановлены")
}

// GetStatus - получить статус записи
func (r *Recorder) GetStatus() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	recording := make([]string, 0)
	for id := range r.processes {
		recording = append(recording, id)
	}

	return map[string]interface{}{
		"running":      r.running,
		"active_count": len(r.processes),
		"recording":    recording,
	}
}

// sanitizeName - очистить название для использования в пути
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "+", "_")
	return name
}
