package config

import (
	"bufio"
	"os"
	"strings"
	"time"
)

// Server-Konfiguration
type Config struct {
	// HTTP-Server
	ListenAddr string

	// Storage
	DataDir string

	// Scraper
	ScrapeInterval time.Duration

	// Agents (vorregistriert)
	Agents []AgentConfig
}

type AgentConfig struct {
	Name    string
	Address string
}

// Default gibt eine sinnvolle Standardkonfiguration zurück
func Default() Config {
	return Config{
		ListenAddr:     ":19999",
		DataDir:        "./data",
		ScrapeInterval: time.Second,
	}
}

// LoadEnv überschreibt Felder mit Umgebungsvariablen
//
//	PULSEWATCH_LISTEN    → ListenAddr
//	PULSEWATCH_DATA_DIR  → DataDir
//	PULSEWATCH_AGENTS    → kommaseparierte Adressen
func LoadEnv(cfg *Config) {
	if v := os.Getenv("PULSEWATCH_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("PULSEWATCH_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("PULSEWATCH_AGENTS"); v != "" {
		for _, addr := range strings.Split(v, ",") {
			addr = strings.TrimSpace(addr)
			if addr != "" {
				cfg.Agents = append(cfg.Agents, AgentConfig{Address: addr})
			}
		}
	}
}

// LoadFile lädt eine einfache Key=Value Konfigdatei
// Kommentare (#) und Leerzeilen werden ignoriert.
//
// Beispiel:
//
//	listen = :19999
//	data_dir = /var/lib/pulsewatch
//	agent = http://web-01:19998
//	agent = http://web-02:19998
func LoadFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch strings.ToLower(key) {
		case "listen":
			cfg.ListenAddr = val
		case "data_dir":
			cfg.DataDir = val
		case "agent":
			cfg.Agents = append(cfg.Agents, AgentConfig{Address: val})
		}
	}
	return scanner.Err()
}
