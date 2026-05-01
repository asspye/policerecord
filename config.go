package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Channel - конфигурация одного канала
type Channel struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	MulticastIP string `yaml:"multicast_ip" json:"multicast_ip"`
	Port        int    `yaml:"port" json:"port"`
	Interface   string `yaml:"interface" json:"interface"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
}
// RecordingConfig - параметры записи
type RecordingConfig struct {
	Bitrate         string `yaml:"bitrate"`
	FPS             string `yaml:"fps"`
	Codec           string `yaml:"codec"`
	Preset          string `yaml:"preset" json:"preset"`
	SegmentDuration int    `yaml:"segment_duration"`
	AudioBitrate    string `yaml:"audio_bitrate"`
	Resolution      string `yaml:"resolution"`
}

// Config - полная конфигурация
type Config struct {
	Channels  []Channel       `yaml:"channels"`
	Recording RecordingConfig `yaml:"recording"`
}

// ConfigManager - менеджер конфигурации с потокобезопасностью
type ConfigManager struct {
	filePath string
	Config   *Config
	mu       sync.RWMutex
}

// NewConfigManager - создание менеджера
func NewConfigManager(filePath string) *ConfigManager {
	return &ConfigManager{
		filePath: filePath,
		Config:   &Config{},
	}
}

// Load - загрузить конфигурацию из файла
func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := os.ReadFile(cm.filePath)
	if err != nil {
		return err
	}

	config := &Config{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return err
	}

	cm.Config = config
	return nil
}

// Save - сохранить конфигурацию в файл
func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := yaml.Marshal(cm.Config)
	if err != nil {
		return err
	}

	return os.WriteFile(cm.filePath, data, 0644)
}

// GetRecordingConfig - получить параметры записи (потокобезопасно)
func (cm *ConfigManager) GetRecordingConfig() RecordingConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.Config.Recording
}

// GetChannels - получить все каналы (потокобезопасно)
func (cm *ConfigManager) GetChannels() []Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channels := make([]Channel, len(cm.Config.Channels))
	copy(channels, cm.Config.Channels)
	return channels
}

// GetChannel - получить канал по ID
func (cm *ConfigManager) GetChannel(id string) *Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for i := range cm.Config.Channels {
		if cm.Config.Channels[i].ID == id {
			ch := cm.Config.Channels[i]
			return &ch
		}
	}
	return nil
}

// UpdateChannel - обновить канал (для API)
// UpdateChannel - обновить канал (для API)
func (cm *ConfigManager) UpdateChannel(id string, updates map[string]interface{}) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	
	for i := range cm.Config.Channels {
		if cm.Config.Channels[i].ID == id {
			// Поддержка обоих вариантов ключей (с большой и маленькой буквы)
			if name, ok := updates["name"].(string); ok {
				cm.Config.Channels[i].Name = name
			}
			if name, ok := updates["Name"].(string); ok {
				cm.Config.Channels[i].Name = name
			}
			
			if ip, ok := updates["multicast_ip"].(string); ok {
				cm.Config.Channels[i].MulticastIP = ip
			}
			if ip, ok := updates["MulticastIP"].(string); ok {
				cm.Config.Channels[i].MulticastIP = ip
			}
			
			if port, ok := updates["port"].(float64); ok {
				cm.Config.Channels[i].Port = int(port)
			}
			if port, ok := updates["Port"].(float64); ok {
				cm.Config.Channels[i].Port = int(port)
			}
			
			if iface, ok := updates["interface"].(string); ok {
				cm.Config.Channels[i].Interface = iface
			}
			if iface, ok := updates["Interface"].(string); ok {
				cm.Config.Channels[i].Interface = iface
			}
			
			if enabled, ok := updates["enabled"].(bool); ok {
				cm.Config.Channels[i].Enabled = enabled
			}
			if enabled, ok := updates["Enabled"].(bool); ok {
				cm.Config.Channels[i].Enabled = enabled
			}
			
			return cm.saveUnlocked()
		}
	}
	return nil
}
// AddChannel - добавить новый канал (для API)
func (cm *ConfigManager) AddChannel(ch Channel) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.Config.Channels = append(cm.Config.Channels, ch)
	return cm.saveUnlocked()
}

// ErrNotFound возвращается когда канал с указанным ID не найден
var ErrNotFound = fmt.Errorf("channel not found")

// DeleteChannel - удалить канал (для API)
func (cm *ConfigManager) DeleteChannel(id string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, ch := range cm.Config.Channels {
		if ch.ID == id {
			cm.Config.Channels = append(cm.Config.Channels[:i], cm.Config.Channels[i+1:]...)
			return cm.saveUnlocked()
		}
	}
	return ErrNotFound
}

// sanitizeName - очистить название для использования в пути к файлу
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "+", "_")
	return name
}

// saveUnlocked - сохранить без блокировки (уже заблокировано)
func (cm *ConfigManager) saveUnlocked() error {
	// Используем encoder для правильного сохранения пустых полей
	file, err := os.Create(cm.filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	
	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2) // Отступ 2 пробела
	
	if err := encoder.Encode(cm.Config); err != nil {
		return err
	}
	
	return encoder.Close()
}
