// Package config loads IgnisForum runtime configuration from a YAML file with
// environment-variable overrides. Values are deliberately minimal for M0 and
// grow with later milestones.
package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the whole application configuration.
type Config struct {
	// ListenAddr is the HTTP listen address, e.g. ":8090".
	ListenAddr string `yaml:"listen_addr"`
	// DataDir is the IgnisDB-Go engine data directory (SSTables + WAL).
	DataDir string `yaml:"data_dir"`
	// MemtableSize bytes flushed to an SSTable. Default 16MB (engine default).
	MemtableSize int64 `yaml:"memtable_size"`
	// CacheSize is the engine hot LRU cache budget in bytes. Fixed 64MB per the
	// architecture rules (§3.7); do not grow this without revisiting the RAM budget.
	CacheSize int `yaml:"cache_size"`
	// CompactionThreshold is the L0 SSTable count that triggers background compaction.
	CompactionThreshold int `yaml:"compaction_threshold"`
	// BaseURL is used for canonical/SEO URLs.
	BaseURL string `yaml:"base_url"`
}

// Default returns configuration with the fixed architecture defaults applied.
func Default() Config {
	return Config{
		ListenAddr:          ":8090",
		DataDir:             "./data",
		MemtableSize:        16 << 20, // 16 MB
		CacheSize:           64 << 20, // 64 MB (fixed budget, §3.7)
		CompactionThreshold: 4,
		BaseURL:             "http://localhost:8090",
	}
}

// Load reads YAML from path (if it exists) and overlays environment overrides.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return cfg, fmt.Errorf("read config: %w", err)
			}
			// Missing file is fine: fall back to defaults + env.
		} else if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	}

	cfg.applyEnv()

	if cfg.CacheSize <= 0 {
		cfg.CacheSize = 64 << 20
	}
	if cfg.MemtableSize <= 0 {
		cfg.MemtableSize = 16 << 20
	}
	if cfg.CompactionThreshold <= 0 {
		cfg.CompactionThreshold = 4
	}
	return cfg, nil
}

// applyEnv overlays IGNISFORUM_* environment variables (env wins over file).
func (c *Config) applyEnv() {
	if v := os.Getenv("IGNISFORUM_LISTEN"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("IGNISFORUM_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("IGNISFORUM_BASE_URL"); v != "" {
		c.BaseURL = v
	}
	if v := os.Getenv("IGNISFORUM_MEMTABLE_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.MemtableSize = n
		}
	}
	if v := os.Getenv("IGNISFORUM_CACHE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CacheSize = n
		}
	}
}
