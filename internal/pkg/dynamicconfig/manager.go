// Package dynamicconfig provides runtime-tunable feature flags with local/etcd backend.
package dynamicconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

const (
	etcdPrefix          = "/app/config/"
	defaultTimeout      = 10 * time.Second
	dynamicConfigPath   = "./configs/dynamic_config.yaml"
	maxBackoffDuration  = 30 * time.Second
	updateDebounceDelay = 1 * time.Second
)

// DynamicConfig holds runtime-changeable features.
type DynamicConfig struct {
	AutoSending            bool   `mapstructure:"auto_sending" json:"auto_sending"`
	RateLimit              int    `mapstructure:"rate_limit" json:"rate_limit"`
	SchedulerQueueCapacity int    `mapstructure:"scheduler_queue_capacity" json:"scheduler_queue_capacity"`
	SchedulerInterval      int    `mapstructure:"scheduler_interval" json:"scheduler_interval"`
	WebhookURL             string `mapstructure:"webhook_url" json:"webhook_url"`
	WebhookAuthKey         string `mapstructure:"webhook_auth_key" json:"webhook_auth_key"`
}

// EtcdClient defines a minimal etcd v3 client interface for abstraction/mocking.
type EtcdClient interface {
	Get(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error)
	Watch(ctx context.Context, key string, opts ...clientv3.OpOption) clientv3.WatchChan
	Close() error
}

// Manager manages loading and watching dynamic config.
type Manager struct {
	mu                sync.RWMutex
	cfg               *DynamicConfig
	logger            *zap.Logger
	etcd              EtcdClient
	lastUpdateTime    time.Time
	updateDebounceDur time.Duration
	stopCh            chan struct{}
}

// NewManager creates a new dynamic config manager.
func NewManager(logger *zap.Logger, etcdEndpoints []string) (*Manager, error) {
	logger.Info("DynamicConfig Manager initialization started", zap.Strings("etcdEndpoints", etcdEndpoints))
	var etcdCli EtcdClient
	if len(etcdEndpoints) > 0 {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   etcdEndpoints,
			DialTimeout: defaultTimeout,
		})
		if err != nil {
			logger.Warn("Etcd connection failed; falling back to local config", zap.Error(err))
			etcdCli = nil
		} else {
			logger.Info("Connected to etcd", zap.Strings("endpoints", etcdEndpoints))
			etcdCli = cli
		}
	}

	m := &Manager{
		cfg:               &DynamicConfig{},
		logger:            logger,
		etcd:              etcdCli,
		updateDebounceDur: updateDebounceDelay,
		stopCh:            make(chan struct{}),
	}
	m.loadFromLocal()
	if m.etcd != nil {
		if err := m.syncEtcd(); err != nil {
			logger.Warn("Initial etcd sync failed", zap.Error(err))
		}
		logger.Info("Initial etcd sync succeeded, starting watcher")
		go m.watchEtcd()
	}
	logger.Info("DynamicConfig Manager initialized")
	return m, nil
}

// loadFromLocal loads config from YAML file and environment variables.
func (m *Manager) loadFromLocal() {
	v := viper.New()
	v.SetConfigFile(dynamicConfigPath)
	v.SetDefault("auto_sending", true)
	v.SetDefault("rate_limit", 2)
	v.SetDefault("scheduler_queue_capacity", 100)
	v.SetDefault("scheduler_interval", 10)
	v.SetDefault("webhook_url", "http://localhost:8080/webhook")
	v.SetDefault("webhook_auth_key", "default_key")

	if err := v.ReadInConfig(); err != nil {
		m.logger.Warn("Local config read failed; using defaults/env", zap.Error(err))
	}
	m.logger.Info("Local config file loaded", zap.String("configFile", v.ConfigFileUsed()))

	tmpCfg := &DynamicConfig{}
	if err := v.Unmarshal(tmpCfg); err != nil {
		m.logger.Warn("Local config unmarshal failed", zap.Error(err))
	} else {
		m.logger.Debug("Local config unmarshaled", zap.Any("config", tmpCfg))
	}

	m.mu.Lock()
	*m.cfg = *tmpCfg
	m.mu.Unlock()

	go m.watchLocalFile()
	m.logger.Info("Starting file watcher goroutine")
	m.logger.Info("Loaded local config", zap.String("source", v.ConfigFileUsed()))
}

// watchLocalFile reloads config on local file changes (debounced).
func (m *Manager) watchLocalFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		m.logger.Error("Failed to create file watcher", zap.Error(err))
		return
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to close watcher: %v\n", err)
		}
	}()

	if err := watcher.Add(dynamicConfigPath); err != nil {
		m.logger.Error("Failed to watch config file", zap.Error(err))
		return
	}

	for {
		select {
		case event := <-watcher.Events:
			if event.Has(fsnotify.Write) && m.shouldDebounce() {
				m.loadFromLocal()
				m.logger.Info("Reloaded local config on change")
			}
		case err := <-watcher.Errors:
			m.logger.Error("File watcher error", zap.Error(err))
		case <-m.stopCh:
			return
		}
	}
}

// syncEtcd does an initial etcd get and applies all keys under etcdPrefix.
func (m *Manager) syncEtcd() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	resp, err := m.etcd.Get(ctx, etcdPrefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	m.updateConfigFromEtcd(resp.Kvs)
	m.logger.Info("Initial etcd config synced", zap.Int("kvCount", len(resp.Kvs)))
	return nil
}

// watchEtcd watches for changes to etcd config and applies them (debounced).
func (m *Manager) watchEtcd() {
	backoff := time.Second
	for {
		watchChan := m.etcd.Watch(context.Background(), etcdPrefix, clientv3.WithPrefix())
		for watchResp := range watchChan {
			if watchResp.Err() != nil {
				m.logger.Error("Etcd watch error; retrying", zap.Error(watchResp.Err()))
				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoffDuration {
					backoff = maxBackoffDuration
				}
				break
			}
			if m.shouldDebounce() {
				for _, ev := range watchResp.Events {
					switch ev.Type {
					case clientv3.EventTypePut:
						m.logger.Info("Applying etcd PUT event", zap.String("key", string(ev.Kv.Key)), zap.ByteString("value", ev.Kv.Value))
						m.updateSingleConfig(string(ev.Kv.Key), ev.Kv.Value)
					case clientv3.EventTypeDelete:
						m.logger.Info("Applying etcd DELETE event", zap.String("key", string(ev.Kv.Key)))
						m.updateSingleConfig(string(ev.Kv.Key), nil)
					}
				}
				m.logger.Info("Processed etcd config update")
			}
		}
	}
}

// updateConfigFromEtcd sets config values from etcd key-value pairs.
func (m *Manager) updateConfigFromEtcd(kvs []*mvccpb.KeyValue) {
	for _, kv := range kvs {
		m.updateSingleConfig(string(kv.Key), kv.Value)
		m.logger.Info("Updated config from etcd", zap.String("key", string(kv.Key)), zap.ByteString("value", kv.Value))
	}
}

// updateSingleConfig sets a single field in DynamicConfig from etcd value.
func (m *Manager) updateSingleConfig(fullKey string, value []byte) {
	key := fullKey[len(etcdPrefix):]
	if key == "" {
		m.logger.Warn("Received empty key in etcd update", zap.String("fullKey", fullKey))
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := reflect.TypeOf(*m.cfg)
	v := reflect.ValueOf(m.cfg).Elem()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == key {
			if len(value) == 0 {
				m.logger.Info("Resetting config field to zero", zap.String("field", field.Name))
				v.Field(i).Set(reflect.Zero(field.Type))
				return
			}
			if err := json.Unmarshal(value, v.Field(i).Addr().Interface()); err != nil {
				m.logger.Warn("Etcd value unmarshal failed", zap.String("key", key), zap.Error(err))
			}
			return
		}
	}
	m.logger.Warn("Ignored unknown etcd config key", zap.String("key", key))
}

// Get returns a copy of the latest dynamic config (thread-safe).
func (m *Manager) Get() DynamicConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.cfg
}

func (m *Manager) shouldDebounce() bool {
	now := time.Now()
	if now.Sub(m.lastUpdateTime) < m.updateDebounceDur {
		return false
	}
	m.lastUpdateTime = now
	return true
}

// Close stops background watchers and closes etcd client if needed.
func (m *Manager) Close() error {
	close(m.stopCh)
	if m.etcd != nil {
		return m.etcd.Close()
	}
	return nil
}
