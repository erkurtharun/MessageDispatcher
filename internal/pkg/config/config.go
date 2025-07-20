package config

import (
	"fmt"
	"net/url"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type StaticConfig struct {
	DB struct {
		URL                string `mapstructure:"url" validate:"omitempty,url"`
		User               string `mapstructure:"user"`
		Password           string `mapstructure:"password"`
		Host               string `mapstructure:"host" validate:"omitempty,hostname"`
		Port               int    `mapstructure:"port" validate:"omitempty,min=1,max=65535"`
		DBName             string `mapstructure:"dbname" validate:"omitempty,alphanum"`
		SSLMode            string `mapstructure:"ssl_mode" validate:"omitempty,oneof=disable require"`
		MaxOpenConnections int    `mapstructure:"max_open_connections" validate:"min=1"`
		MaxIdleConnections int    `mapstructure:"max_idle_connections" validate:"min=1"`
		ConnMaxLifetime    int    `mapstructure:"conn_max_lifetime" validate:"min=60"`
	} `mapstructure:"db"`
	Redis struct {
		Addr               string `mapstructure:"addr" validate:"required"`
		Password           string `mapstructure:"password"`
		DB                 int    `mapstructure:"db" validate:"min=0"`
		PoolSize           int    `mapstructure:"pool_size" validate:"min=10"`
		MinIdleConnections int    `mapstructure:"min_idle_connections" validate:"min=5"`
		MaxRetries         int    `mapstructure:"max_retries" validate:"min=0"`
	} `mapstructure:"redis"`
	Port   string `mapstructure:"port" validate:"required"`
	Server struct {
		ReadTimeout  int `mapstructure:"read_timeout" validate:"min=5"`
		WriteTimeout int `mapstructure:"write_timeout" validate:"min=10"`
		IdleTimeout  int `mapstructure:"idle_timeout" validate:"min=30"`
	} `mapstructure:"server"`
	Remote struct {
		EtcdEndpoints []string `mapstructure:"etcd_endpoints"`
	} `mapstructure:"remote"`
}

var binds = []string{
	"db.url", "db.user", "db.password", "db.host", "db.port", "db.dbname", "db.ssl_mode",
	"db.max_open_connections", "db.max_idle_connections", "db.conn_max_lifetime",
	"redis.addr", "redis.password", "redis.db", "redis.pool_size", "redis.min_idle_connections",
	"redis.max_retries", "port", "server.read_timeout", "server.write_timeout",
	"server.idle_timeout", "remote.etcd_endpoints",
}

var defaultsMap = map[string]interface{}{
	"db.host": "localhost", "db.port": 5432, "db.dbname": "postgres", "db.ssl_mode": "disable",
	"db.max_open_connections": 100, "db.max_idle_connections": 10, "db.conn_max_lifetime": 300,
	"redis.addr": "localhost:6379", "redis.password": "", "redis.db": 0, "redis.pool_size": 20,
	"redis.min_idle_connections": 5, "redis.max_retries": 3, "port": ":8080",
	"server.read_timeout": 10, "server.write_timeout": 30, "server.idle_timeout": 60,
	"remote.etcd_endpoints": []string{"localhost:2379"},
}

type Loader struct {
	once     sync.Once
	mu       sync.RWMutex
	config   *StaticConfig
	logger   *zap.Logger
	env      string
	validate *validator.Validate
}

func NewConfigLoader(env string, logger *zap.Logger) *Loader {
	return &Loader{
		env:      env,
		logger:   logger,
		validate: validator.New(),
	}
}

func (cl *Loader) Load() (*StaticConfig, error) {
	var err error

	cl.once.Do(func() {
		v := viper.New()
		v.SetConfigName("app." + cl.env)
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AutomaticEnv()
		v.SetEnvPrefix("APP")

		for _, key := range binds {
			if bindErr := v.BindEnv(key); bindErr != nil {
				cl.logger.Error("Failed to bind env", zap.String("key", key), zap.Error(bindErr))
				err = fmt.Errorf("failed to bind env for %s: %w", key, bindErr)
				return
			}
		}
		for key, val := range defaultsMap {
			v.SetDefault(key, val)
		}
		if readErr := v.ReadInConfig(); readErr != nil {
			cl.logger.Warn("Config file not found, using env/defaults", zap.String("file", "app."+cl.env+".yaml"), zap.Error(readErr))
		}
		tmpCfg := &StaticConfig{}
		if unmarshalErr := v.Unmarshal(tmpCfg); unmarshalErr != nil {
			cl.logger.Error("Config unmarshal failed", zap.Error(unmarshalErr))
			err = fmt.Errorf("failed to unmarshal config: %w", unmarshalErr)
			return
		}
		if tmpCfg.DB.URL == "" {
			if tmpCfg.DB.User == "" || tmpCfg.DB.Host == "" || tmpCfg.DB.DBName == "" {
				cl.logger.Error("DB URL and components missing",
					zap.String("user", tmpCfg.DB.User),
					zap.String("host", tmpCfg.DB.Host),
					zap.String("dbname", tmpCfg.DB.DBName),
				)
				err = fmt.Errorf("db.url not set and required DB components are missing")
				return
			}
			tmpCfg.DB.URL = makePostgresURL(tmpCfg)
			cl.logger.Info("DB URL constructed from components", zap.String("url", tmpCfg.DB.URL))
		}
		if validationErr := cl.validate.Struct(tmpCfg); validationErr != nil {
			cl.logger.Error("Config validation failed", zap.Error(validationErr))
			err = fmt.Errorf("config validation failed: %w", validationErr)
			return
		}
		cl.mu.Lock()
		cl.config = tmpCfg
		cl.mu.Unlock()
		cl.logger.Info("Static config loaded successfully")
	})

	return cl.Get(), err
}

func (cl *Loader) Get() *StaticConfig {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	return cl.config
}

func makePostgresURL(cfg *StaticConfig) string {
	pass := url.QueryEscape(cfg.DB.Password)
	port := ""
	if cfg.DB.Port > 0 {
		port = fmt.Sprintf(":%d", cfg.DB.Port)
	}
	ssl := cfg.DB.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	return fmt.Sprintf("postgres://%s:%s@%s%s/%s?sslmode=%s",
		cfg.DB.User, pass, cfg.DB.Host, port, cfg.DB.DBName, ssl)
}
