package main

import (
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/go-kit/kit/log/level"
	"github.com/go-kit/log"
	"gopkg.in/yaml.v3"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// OrgConfig represents the configuration for single Grafana Org users in Loki
type OrgConfig struct {
	Org   string                       `yaml:"org"`
	Users map[string]map[string]string `yaml:"users"`
}

type Config struct {
	logger log.Logger
	paths  *[]string
	orgs   map[string]OrgConfig
}

func newConfig(paths *[]string, logger log.Logger) *Config {
	config := &Config{
		logger: logger,
		paths:  paths,
		orgs:   map[string]OrgConfig{},
	}
	config.watch()

	return config
}

func (c *Config) watch() {
	sgnl := make(chan os.Signal, 1)
	signal.Notify(sgnl, syscall.SIGINT, syscall.SIGTERM)

	delayTimer := time.NewTimer(0)
	changed := pathNotifier(c.paths, c.logger)

	go func() {
		for {
			select {
			case <-sgnl:
				level.Info(c.logger).Log("msg", "Received SIGINT or SIGTERM. Shutting down")
				os.Exit(0)
			case <-changed:
				level.Debug(c.logger).Log("msg", "Got a config change event, waiting for 5s before reloading")
				delayTimer.Reset(5 * time.Second)

			case <-delayTimer.C:
				level.Info(c.logger).Log("msg", "Reloading configs")
				c.reload()
			}
		}
	}()
}

func (c *Config) reload() {
	orgsLoaded := map[string]bool{}
	for _, path := range *c.paths {
		// could be specified a file directly, or dir with number of files. Don't recurse into subdirs
		files := []string{path}
		fi, _ := os.Stat(path)
		if fi.IsDir() {
			entries, err := os.ReadDir(path)
			if err != nil {
				level.Error(c.logger).Log("msg", "Error reading directory", "path", path, "err", err)
				os.Exit(1)
			}
			files = []string{}
			for _, file := range entries {
				i, err := os.Stat(path + "/" + file.Name())
				if err == nil && i.Mode().IsRegular() {
					files = append(files, path+"/"+file.Name())
				}
			}
		}
		for _, file := range files {
			cfg, err := loadFile(file)
			if err != nil {
				level.Error(c.logger).Log("msg", "Error reading config", "file", file, "err", err)
				os.Exit(1)
			}
			level.Info(c.logger).Log("msg", "Loaded config", "file", file)
			// TODO: RWMutex?
			c.orgs[cfg.Org] = cfg
			orgsLoaded[cfg.Org] = true
		}
	}
	// Remove orgs that are no longer in the configs
	for org := range c.orgs {
		if _, ok := orgsLoaded[org]; !ok {
			delete(c.orgs, org)
		}
	}
	level.Debug(c.logger).Log("msg", fmt.Sprintf("Config reloaded: %v", c.orgs))
	if len(c.orgs) == 0 {
		level.Error(c.logger).Log("msg", "No Orgs loaded from configs, exiting")
		os.Exit(1)
	}
}

func loadFile(path string) (cfg OrgConfig, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.Org) == "" {
		return cfg, fmt.Errorf("`org` must be set")
	}
	if _, ok := cfg.Users["default"]; !ok {
		return cfg, fmt.Errorf("no user `default` is defined")
	}
	return cfg, nil
}

func pathNotifier(paths *[]string, logger log.Logger) chan bool {
	changed := make(chan bool)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		level.Error(logger).Log("msg", "Error creating watcher", "err", err)
		os.Exit(1)
	}

	for _, path := range *paths {
		err = watcher.Add(path)
		if err != nil {
			level.Error(logger).Log("msg", "Error adding path to watcher", "err", err)
			os.Exit(1)
		}
	}

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Write == fsnotify.Write ||
					event.Op&fsnotify.Create == fsnotify.Create {
					changed <- true
				}
			case err := <-watcher.Errors:
				level.Error(logger).Log("msg", "Watcher error", "err", err)
			}
		}
	}()
	return changed
}
