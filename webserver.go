package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WebServer - структура веб-сервера
type WebServer struct {
	configManager *ConfigManager
	logger        *log.Logger
	server        *http.Server
}

// NewWebServer - создание веб-сервера
func NewWebServer(cm *ConfigManager, logger *log.Logger, port string) *WebServer {
	ws := &WebServer{
		configManager: cm,
		logger:        logger,
	}

	mux := http.NewServeMux()

	// API маршруты с CORS (порядок важен!)
	mux.HandleFunc("/api/channels/", ws.addCORS(ws.handleChannelsWithID))
	mux.HandleFunc("/api/channels", ws.addCORS(ws.handleChannels))
	mux.HandleFunc("/api/interfaces", ws.addCORS(ws.handleInterfaces))
	mux.HandleFunc("/api/files/", ws.addCORS(ws.handleFiles))
	mux.HandleFunc("/api/stream/", ws.addCORS(ws.handleStream))
	mux.HandleFunc("/api/download/", ws.addCORS(ws.handleDownload))
	mux.HandleFunc("/api/archive/", ws.addCORS(ws.handleArchive))
	mux.HandleFunc("/api/status", ws.addCORS(ws.handleStatus))
	mux.HandleFunc("/", ws.handleRoot)

	ws.server = &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	return ws
}

// addCORS - добавить CORS заголовки
func (ws *WebServer) addCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// Start - запустить веб-сервер
func (ws *WebServer) Start() error {
	ws.logger.Printf("🌐 API сервер запущен на http://0.0.0.0%s", ws.server.Addr)
	return ws.server.ListenAndServe()
}

// Stop - остановить веб-сервер
func (ws *WebServer) Stop() {
	if ws.server != nil {
		ws.server.Close()
	}
}

// handleRoot - корневой путь
// handleRoot - главная страница
func (ws *WebServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Если запрашивают не корень - вернуть 404
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	
	// Прочитать HTML файл
	htmlContent, err := os.ReadFile("frontend.html")
	if err != nil {
		// Если файла нет - вернуть JSON с ошибкой
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "HTML file not found: " + err.Error(),
		})
		return
	}
	
	// Отдать HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(htmlContent)
}
// handleChannels - GET все каналы, POST добавить
func (ws *WebServer) handleChannels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		channels := ws.configManager.GetChannels()
		json.NewEncoder(w).Encode(channels)

	case http.MethodPost:
		var ch Channel
		if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := validateChannel(ch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Проверить дублирование ID если указан явно
		if ch.ID != "" && ws.configManager.GetChannel(ch.ID) != nil {
			http.Error(w, "Channel with this ID already exists", http.StatusConflict)
			return
		}

		// Генерировать ID если не указан
		if ch.ID == "" {
			// Найти максимальный номер среди существующих каналов
			maxNum := 0
			for _, existing := range ws.configManager.GetChannels() {
				var num int
				if _, err := fmt.Sscanf(existing.ID, "channel_%d", &num); err == nil {
					if num > maxNum {
						maxNum = num
					}
				}
			}
			ch.ID = fmt.Sprintf("channel_%d", maxNum+1)
		}

		if err := ws.configManager.AddChannel(ch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ws.logger.Printf("➕ Добавлен канал: %s", ch.Name)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ch)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleChannelsWithID - обработка PUT/DELETE с ID в URL
func (ws *WebServer) handleChannelsWithID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Извлечь ID из пути
	channelID := strings.TrimPrefix(r.URL.Path, "/api/channels/")
	if channelID == "" {
		http.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Вернуть один канал
		ch := ws.configManager.GetChannel(channelID)
		if ch == nil {
			http.Error(w, "Channel not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(ch)

	case http.MethodPut:
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := ws.configManager.UpdateChannel(channelID, updates); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ws.logger.Printf("✏️  Обновлён канал: %s", channelID)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": channelID})

	case http.MethodDelete:
		if err := ws.configManager.DeleteChannel(channelID); err == ErrNotFound {
			http.Error(w, "Channel not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ws.logger.Printf("🗑️  Удалён канал: %s", channelID)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": channelID})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleInterfaces - получить список сетевых интерфейсов
func (ws *WebServer) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	interfaces, err := net.Interfaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ifaceNames := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Name != "lo" {
			ifaceNames = append(ifaceNames, iface.Name)
		}
	}

	json.NewEncoder(w).Encode(map[string][]string{"interfaces": ifaceNames})
}

// FileInfo - информация о файле
type FileInfo struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Modified    int64  `json:"modified"`
	ModifiedStr string `json:"modified_str"`
}

// handleFiles - получить список файлов канала
func (ws *WebServer) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	channelName := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if channelName == "" {
		http.Error(w, "Channel name required", http.StatusBadRequest)
		return
	}

	// Очистить название так же как в рекордере
	safeName := sanitizeName(channelName)
	channelDir := filepath.Join("/opt/tv-recorder/recordings", safeName)

	entries, err := os.ReadDir(channelDir)
	if err != nil {
		json.NewEncoder(w).Encode(map[string][]FileInfo{"files": []FileInfo{}})
		return
	}

	files := make([]FileInfo, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			info, _ := entry.Info()
			files = append(files, FileInfo{
				Name:        entry.Name(),
				Size:        info.Size(),
				Modified:    info.ModTime().Unix(),
				ModifiedStr: info.ModTime().Format("02.01.2006 15:04:05"),
			})
		}
	}

	// Сортировать по времени (новые первые)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Modified > files[j].Modified
	})

	json.NewEncoder(w).Encode(map[string][]FileInfo{"files": files})
}

// handleStream - стриминг видео
func (ws *WebServer) handleStream(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/stream/")
	parts := strings.SplitN(path, "/", 2)
	
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	
	channelName := parts[0]
	filename := parts[1]
	
	// Очистить название канала так же как в рекордере
	safeName := sanitizeName(channelName)
	filePath := filepath.Join("/opt/tv-recorder/recordings", safeName, filename)

	if !isFileSafe(filePath, "/opt/tv-recorder/recordings") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "video/mp2t")
	http.ServeContent(w, r, filename, time.Time{}, file)
}

// handleArchive - скачать файлы за дату в ZIP
func (ws *WebServer) handleArchive(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/archive/")
	parts := strings.SplitN(path, "/", 2)
	
	if len(parts) != 2 {
		http.Error(w, "Invalid path. Use: /api/archive/ChannelName/2025-12-04", http.StatusBadRequest)
		return
	}
	
	channelName := parts[0]
	dateStr := parts[1] // Формат: 2025-12-04

	if _, err := time.Parse("2006-01-02", dateStr); err != nil {
		http.Error(w, "Invalid date format. Use YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	
	safeName := sanitizeName(channelName)
	channelDir := filepath.Join("/opt/tv-recorder/recordings", safeName)
	
	// Найти файлы за указанную дату
	entries, err := os.ReadDir(channelDir)
	if err != nil {
		http.Error(w, "Channel not found", http.StatusNotFound)
		return
	}
	
	// Фильтр по дате
	var matchedFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			// Проверить что имя файла содержит дату
			if strings.Contains(entry.Name(), dateStr) {
				matchedFiles = append(matchedFiles, filepath.Join(channelDir, entry.Name()))
			}
		}
	}
	
	if len(matchedFiles) == 0 {
		http.Error(w, "No files found for this date", http.StatusNotFound)
		return
	}
	
	// Создать информативное имя архива: ChannelName_YYYY-MM-DD_24files.zip
	archiveName := fmt.Sprintf("%s_%s_%dfiles.zip", safeName, dateStr, len(matchedFiles))
	
	// Создать временный ZIP
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", archiveName))
	
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()
	
	for _, filePath := range matchedFiles {
		func() {
			file, err := os.Open(filePath)
			if err != nil {
				return
			}
			defer file.Close()

			info, _ := file.Stat()
			header, _ := zip.FileInfoHeader(info)
			header.Name = info.Name()
			header.Method = zip.Store // Без сжатия (видео уже сжато)

			writer, _ := zipWriter.CreateHeader(header)
			io.Copy(writer, file)
		}()
	}
	
	ws.logger.Printf("📦 Создан архив: %s за %s (%d файлов)", safeName, dateStr, len(matchedFiles))
}

// handleDownload - скачивание файла
func (ws *WebServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/download/")
	parts := strings.SplitN(path, "/", 2)
	
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	
	channelName := parts[0]
	filename := parts[1]
	
	// Очистить название канала так же как в рекордере
	safeName := sanitizeName(channelName)
	filePath := filepath.Join("/opt/tv-recorder/recordings", safeName, filename)

	if !isFileSafe(filePath, "/opt/tv-recorder/recordings") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, file)
	
	ws.logger.Printf("📥 Скачан файл: %s/%s", safeName, filename)
}
// handleStatus - статус системы
func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Подсчитать записи
	totalFiles := 0
	totalSize := int64(0)

	filepath.Walk("/opt/tv-recorder/recordings", func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".ts") {
			totalFiles++
			totalSize += info.Size()
		}
		return nil
	})

	channels := ws.configManager.GetChannels()
	activeCount := 0
	for _, ch := range channels {
		if ch.Enabled {
			activeCount++
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "ok",
		"timestamp":       time.Now().Unix(),
		"total_files":     totalFiles,
		"total_size":      totalSize,
		"total_size_mb":   float64(totalSize) / 1024 / 1024,
		"active_channels": activeCount,
		"total_channels":  len(channels),
	})
}

// isFileSafe - проверить что файл в безопасной директории
func isFileSafe(filePath, baseDir string) bool {
	absFile, _ := filepath.Abs(filePath)
	absBase, _ := filepath.Abs(baseDir)
	return strings.HasPrefix(absFile, absBase)
}

// validateChannel - базовая валидация параметров канала
func validateChannel(ch Channel) error {
	if ch.Name == "" {
		return fmt.Errorf("name is required")
	}
	if net.ParseIP(ch.MulticastIP) == nil {
		return fmt.Errorf("invalid multicast_ip: %q", ch.MulticastIP)
	}
	if ch.Port < 1 || ch.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

