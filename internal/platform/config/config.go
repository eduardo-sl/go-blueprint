package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	Env          string        `mapstructure:"env"`
	Addr         string        `mapstructure:"addr"`
	DatabaseURL  string        `mapstructure:"database_url"`
	EventLogPath string        `mapstructure:"event_log_path"`
	JWTSecret    string        `mapstructure:"jwt_secret"`
	JWTExpiry    time.Duration `mapstructure:"jwt_expiry"`
	LogLevel     string        `mapstructure:"log_level"`
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("env", "development")
	v.SetDefault("addr", ":8080")
	v.SetDefault("event_log_path", "./data/events.db")
	v.SetDefault("jwt_expiry", "24h")
	v.SetDefault("log_level", "info")

	_ = v.ReadInConfig()

	if err := v.BindEnv("database_url", "DATABASE_URL"); err != nil {
		return nil, fmt.Errorf("config: bind DATABASE_URL: %w", err)
	}
	if err := v.BindEnv("jwt_secret", "JWT_SECRET"); err != nil {
		return nil, fmt.Errorf("config: bind JWT_SECRET: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return fmt.Errorf("config: JWT_SECRET is required")
	}
	return nil
}
