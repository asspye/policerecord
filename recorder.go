package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stallTimeout = 60 * time.Second // видеокадров нет дольше — ffmpeg считается зависшим
	stopTimeout  = 10 * time.Second // после SIGINT ждём столько, затем Kill
	healthyRun   = 10 * time.Minute // ffmpeg проработал дольше — счётчик ошибок сбрасывается
)

var errVideoStalled = errors.New("видео зависло")

// Recorder - структура рекордера
type Recorder struct {
	configManager   *ConfigManager
	logger          *log.Logger
	processes       map[string]*exec.Cmd
	stopChans       map[string]chan bool
	runningChannels map[string]Channel       // параметры с которыми запущен каждый канал
	done            map[string]chan struct{} // закрывается, когда горутина канала завершилась
	mu              sync.RWMutex
	running         bool
}

// NewRecorder - создание нового рекордера
func NewRecorder(cm *ConfigManager, logger *log.Logger) *Recorder {
	return &Recorder{
		configManager:   cm,
		logger:          logger,
		processes:       make(map[string]*exec.Cmd),
		stopChans:       make(map[string]chan bool),
		runningChannels: make(map[string]Channel),
		done:            make(map[string]chan struct{}),
		running:         false,
	}
}

// frameWatcher разбирает вывод `ffmpeg -progress` и запоминает,
// когда в последний раз вырос счётчик видеокадров.
type frameWatcher struct {
	mu      sync.Mutex
	buf     []byte
	frame   int64
	changed time.Time
}

func (f *frameWatcher) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(f.buf[:i]))
		f.buf = f.buf[i+1:]
		if v, ok := strings.CutPrefix(line, "frame="); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n != f.frame {
				f.frame = n
				f.changed = time.Now()
			}
		}
	}
	return len(p), nil
}

// idle - последний номер кадра и сколько времени он не меняется
func (f *frameWatcher) idle() (int64, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frame, time.Since(f.changed)
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
				r.startChannel(ch)
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

	var stopping []chan struct{}
	for id, stopChan := range currentChannels {
		newCh, shouldRun := activeChannels[id]

		needRestart := false
		if shouldRun {
			r.mu.RLock()
			old := r.runningChannels[id]
			r.mu.RUnlock()
			if old.MulticastIP != newCh.MulticastIP || old.Port != newCh.Port || old.Interface != newCh.Interface {
				r.logger.Printf("🔁 Параметры канала изменились, перезапуск: %s", id)
				needRestart = true
			}
		}

		if !shouldRun || needRestart {
			if !shouldRun {
				r.logger.Printf("⏸️  Остановка канала: %s", id)
			}
			r.mu.Lock()
			if !r.running {
				// Stop() уже закрыл все каналы — выходим, чтобы не сделать double-close
				r.mu.Unlock()
				return
			}
			delete(r.stopChans, id)
			delete(r.runningChannels, id)
			if d, ok := r.done[id]; ok {
				stopping = append(stopping, d)
			}
			r.mu.Unlock()

			close(stopChan)
		}
	}

	// Дождаться остановки старых ffmpeg, чтобы при смене параметров
	// не писали два процесса в одну папку
	for _, d := range stopping {
		<-d
	}

	// Запустить новые и перезапущенные каналы
	for id, ch := range activeChannels {
		r.mu.RLock()
		_, exists := r.stopChans[id]
		r.mu.RUnlock()

		if !exists {
			r.startChannel(ch)
			r.logger.Printf("▶️  Запущен канал: %s", ch.Name)
		}
	}
}

// startChannel - запустить горутину записи канала
func (r *Recorder) startChannel(ch Channel) {
	stopChan := make(chan bool)
	done := make(chan struct{})
	r.mu.Lock()
	r.stopChans[ch.ID] = stopChan
	r.runningChannels[ch.ID] = ch
	r.done[ch.ID] = done
	r.mu.Unlock()

	go r.recordChannel(ch, stopChan, done)
}

// recordChannel - запись одного канала (работает в отдельной горутине)
func (r *Recorder) recordChannel(ch Channel, stopChan chan bool, done chan struct{}) {
	defer close(done)

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

		// Открыть лог FFmpeg — закрываем явно после завершения, не через defer (цикл)
		var logF *os.File
		logPath := filepath.Join("logs", fmt.Sprintf("ffmpeg_%s.log", ch.ID))
		if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); ferr == nil {
			cmd.Stderr = f
			logF = f
		}

		watcher := &frameWatcher{changed: time.Now()}
		cmd.Stdout = watcher // сюда идёт -progress

		r.logger.Printf("▶️  [%s] Запуск FFmpeg...", ch.Name)

		// Запустить процесс
		started := time.Now()
		stopped := false
		err := cmd.Start()
		if err == nil {
			r.mu.Lock()
			r.processes[ch.ID] = cmd
			r.mu.Unlock()

			stopped, err = r.waitFFmpeg(ch, cmd, watcher, stopChan)

			r.mu.Lock()
			delete(r.processes, ch.ID)
			r.mu.Unlock()
		}

		if logF != nil {
			logF.Close()
			logF = nil
		}

		if stopped {
			r.logger.Printf("🛑 Остановка записи: %s", ch.Name)
			return
		}

		if err != nil {
			// Долго проработавший ffmpeg — не повод копить попытки
			if time.Since(started) >= healthyRun {
				retryCount = 0
			}
			retryCount++
			r.logger.Printf("⚠️  [%s] Ошибка FFmpeg: %v (попытка %d/%d)",
				ch.Name, err, retryCount, maxRetries)

			if retryCount >= maxRetries {
				r.logger.Printf("❌ [%s] Превышено количество попыток. Канал остановлен.", ch.Name)
				r.mu.Lock()
				delete(r.stopChans, ch.ID)
				delete(r.runningChannels, ch.ID)
				r.mu.Unlock()
				return
			}
		} else {
			retryCount = 0 // Сбросить счётчик при успешной работе
		}

		r.logger.Printf("🔄 [%s] Перезапуск через %v...", ch.Name, retryDelay)
		select {
		case <-stopChan:
			r.logger.Printf("🛑 Остановка записи: %s", ch.Name)
			return
		case <-time.After(retryDelay):
		}
	}
}

// waitFFmpeg ждёт завершения ffmpeg. По команде остановки (stopped=true) или
// если видеокадров нет дольше stallTimeout, просит ffmpeg завершиться.
func (r *Recorder) waitFFmpeg(ch Channel, cmd *exec.Cmd, w *frameWatcher, stopChan chan bool) (stopped bool, err error) {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	check := time.NewTicker(10 * time.Second)
	defer check.Stop()

	for {
		select {
		case err := <-exited:
			return false, err
		case <-stopChan:
			r.interruptFFmpeg(cmd, exited)
			return true, nil
		case <-check.C:
			if frame, idle := w.idle(); idle > stallTimeout {
				r.logger.Printf("🧊 [%s] Нет видеокадров %v (frame=%d), перезапуск FFmpeg",
					ch.Name, idle.Round(time.Second), frame)
				r.interruptFFmpeg(cmd, exited)
				return false, errVideoStalled
			}
		}
	}
}

// interruptFFmpeg - SIGINT (ffmpeg корректно закрывает текущий сегмент),
// через stopTimeout — Kill. Возвращается после завершения процесса.
func (r *Recorder) interruptFFmpeg(cmd *exec.Cmd, exited chan error) {
	cmd.Process.Signal(os.Interrupt)
	select {
	case <-exited:
	case <-time.After(stopTimeout):
		cmd.Process.Kill()
		<-exited
	}
}

// buildFFmpegCommand - построить команду FFmpeg
func (r *Recorder) buildFFmpegCommand(ch Channel, outputDir string) *exec.Cmd {
	rec := r.configManager.GetRecordingConfig()

	// Входной URL (мультикаст) с указанием локального адреса интерфейса
	inputURL := fmt.Sprintf("udp://%s:%d?localaddr=%s&fifo_size=50000000&overrun_nonfatal=1&timeout=5000000",
		ch.MulticastIP, ch.Port, r.getInterfaceIP(ch.Interface))

	channelName := sanitizeName(ch.Name)

	// Шаблон для имени файла
	outputPattern := filepath.Join(outputDir, fmt.Sprintf("%s_%%Y-%%m-%%d_%%H-%%M-%%S.ts", channelName))

	var args []string
	if rec.HWAccel == "vaapi" {
		args = vaapiArgs(rec, inputURL, channelName)
	} else {
		args = cpuArgs(rec, inputURL, channelName)
	}

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

	// -progress: номер кадра для проверки зависания видео (frameWatcher)
	return exec.Command("ffmpeg", append([]string{"-progress", "pipe:1"}, args...)...)
}

// overlayFilter - наложение даты/времени и названия канала.
func overlayFilter(channelName string, timeSize, nameSize, x, timeY, nameY, border int) string {
	const font = "fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:"
	return "drawtext=" + font +
		"text='%{localtime}': " +
		fmt.Sprintf("fontsize=%d:fontcolor=white:x=%d:y=%d:", timeSize, x, timeY) +
		fmt.Sprintf("box=1:boxcolor=black@0.7:boxborderw=%d", border) +
		",drawtext=" + font +
		fmt.Sprintf("text='%s':", channelName) +
		fmt.Sprintf("fontsize=%d:fontcolor=yellow:x=%d:y=%d:", nameSize, x, nameY) +
		fmt.Sprintf("box=1:boxcolor=black@0.7:boxborderw=%d", border)
}

// cpuArgs - всё на CPU: текст рисуется на исходном кадре, затем кадр уменьшается до rec.Resolution.
func cpuArgs(rec RecordingConfig, inputURL, channelName string) []string {
	args := []string{
		"-fflags", "+genpts+discardcorrupt",
		"-err_detect", "ignore_err",
		"-i", inputURL,
		"-vf", overlayFilter(channelName, 28, 24, 10, 10, 50, 5),
		"-c:v", rec.Codec,
		"-b:v", rec.Bitrate,
		"-s", rec.Resolution,
		"-r", rec.FPS,
	}
	if rec.Codec == "libx264" && rec.Preset != "" {
		args = append(args, "-preset", rec.Preset)
	}
	return args
}

// vaapiArgs - декодирование, деинтерлейс, fps и масштабирование на GPU; кадр уже в rec.Resolution
// выгружается в память для drawtext (на CPU, дёшево) и загружается обратно для h264_vaapi.
// Шрифт мельче, чем в cpuArgs: там текст рисуется на 1080p и потом уменьшается вместе с кадром.
func vaapiArgs(rec RecordingConfig, inputURL, channelName string) []string {
	device := rec.VAAPIDevice
	if device == "" {
		device = "/dev/dri/renderD128"
	}
	w, h := "640", "480"
	if parts := strings.SplitN(rec.Resolution, "x", 2); len(parts) == 2 {
		w, h = parts[0], parts[1]
	}
	vf := "deinterlace_vaapi=auto=1," +
		"fps=" + rec.FPS + "," +
		"scale_vaapi=w=" + w + ":h=" + h + ":format=nv12," +
		"hwdownload,format=nv12,format=yuv420p," +
		overlayFilter(channelName, 16, 14, 6, 6, 30, 3) + "," +
		"format=nv12,hwupload"
	return []string{
		"-fflags", "+genpts+discardcorrupt",
		"-err_detect", "ignore_err",
		"-init_hw_device", "vaapi=va:" + device,
		"-hwaccel", "vaapi",
		"-hwaccel_device", "va",
		"-hwaccel_output_format", "vaapi",
		"-filter_hw_device", "va",
		"-i", inputURL,
		"-vf", vf,
		"-c:v", "h264_vaapi",
		"-b:v", rec.Bitrate,
	}
}

// getOutputDir - получить путь для записи
func (r *Recorder) getOutputDir(channelName string) string {
	safeName := sanitizeName(channelName)
	return filepath.Join("/opt/tv-recorder/recordings", safeName)
}

// getInterfaceIP - получить IPv4 адрес сетевого интерфейса через stdlib
func (r *Recorder) getInterfaceIP(ifaceName string) string {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		r.logger.Printf("⚠️  Интерфейс не найден %s: %v", ifaceName, err)
		return "0.0.0.0"
	}

	addrs, err := iface.Addrs()
	if err != nil {
		r.logger.Printf("⚠️  Не удалось получить адреса для %s: %v", ifaceName, err)
		return "0.0.0.0"
	}

	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}

	r.logger.Printf("⚠️  IPv4 не найден для %s, использую 0.0.0.0", ifaceName)
	return "0.0.0.0"
}

// Stop - остановить все записи
func (r *Recorder) Stop() {
	r.logger.Println("🛑 Остановка системы записи...")

	r.mu.Lock()
	r.running = false
	stopChans := r.stopChans
	dones := r.done
	r.stopChans = make(map[string]chan bool)
	r.runningChannels = make(map[string]Channel)
	r.done = make(map[string]chan struct{})
	r.mu.Unlock()

	// Отправить сигнал остановки всем каналам — каждый сам останавливает свой ffmpeg
	for _, stopChan := range stopChans {
		close(stopChan)
	}

	deadline := time.After(stopTimeout + 5*time.Second)
wait:
	for _, d := range dones {
		select {
		case <-d:
		case <-deadline:
			break wait
		}
	}

	// Принудительно убить оставшиеся процессы FFmpeg
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
