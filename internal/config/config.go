// Package config holds the persisted application settings.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type ArrKind string

const (
	Sonarr ArrKind = "sonarr"
	Radarr ArrKind = "radarr"
)

// ArrInstance is a single Sonarr or Radarr server.
type ArrInstance struct {
	ID      string  `json:"id"`
	Kind    ArrKind `json:"kind"`
	Name    string  `json:"name"`
	URL     string  `json:"url"`
	APIKey  string  `json:"apiKey"`
	Enabled bool    `json:"enabled"`
}

// PathMapping rewrites a path prefix as seen by a remote service into the
// path as seen by cleanarr.
type PathMapping struct {
	Remote string `json:"remote"`
	Local  string `json:"local"`
}

// QbitInstance is a qBittorrent Web UI endpoint.
type QbitInstance struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	URL          string        `json:"url"`
	Username     string        `json:"username"`
	Password     string        `json:"password"`
	Enabled      bool          `json:"enabled"`
	Categories   []string      `json:"categories"`   // empty = all categories
	PathMappings []PathMapping `json:"pathMappings"` // qBittorrent path -> local path
}

type Config struct {
	Sonarr []ArrInstance  `json:"sonarr"`
	Radarr []ArrInstance  `json:"radarr"`
	Qbit   []QbitInstance `json:"qbittorrent"`

	// ExtraLibraryPaths are scanned in addition to the *arr root folders.
	ExtraLibraryPaths []string `json:"extraLibraryPaths"`
	// DownloadPaths are scanned for hardlinks and leftover downloads.
	DownloadPaths []string `json:"downloadPaths"`
	// ExcludedPaths are never reported or deleted.
	ExcludedPaths []string `json:"excludedPaths"`
	// IgnoredNames are file/directory names skipped during scans (e.g. @eaDir).
	IgnoredNames []string `json:"ignoredNames"`
	// MinAgeHours hides anything modified or added more recently than this.
	MinAgeHours int `json:"minAgeHours"`
	// ScanIntervalHours triggers an automatic scan; 0 disables it.
	ScanIntervalHours int `json:"scanIntervalHours"`
	// IncludeRecycleBins reports the contents of the *arr recycle bins.
	IncludeRecycleBins bool `json:"includeRecycleBins"`
}

func Default() Config {
	return Config{
		Sonarr:             []ArrInstance{},
		Radarr:             []ArrInstance{},
		Qbit:               []QbitInstance{},
		ExtraLibraryPaths:  []string{},
		DownloadPaths:      []string{},
		ExcludedPaths:      []string{},
		IgnoredNames:       []string{"@eaDir", "#recycle", "#snapshot", ".snapshot", ".snapshots", "lost+found", ".DS_Store", ".Trash-1000"},
		MinAgeHours:        24,
		ScanIntervalHours:  0,
		IncludeRecycleBins: true,
	}
}

// Arrs returns all enabled *arr instances.
func (c *Config) Arrs() []ArrInstance {
	var out []ArrInstance
	for _, list := range [][]ArrInstance{c.Sonarr, c.Radarr} {
		for _, a := range list {
			if a.Enabled {
				out = append(out, a)
			}
		}
	}
	return out
}

func (c *Config) EnabledQbit() []QbitInstance {
	var out []QbitInstance
	for _, q := range c.Qbit {
		if q.Enabled {
			out = append(out, q)
		}
	}
	return out
}

// Validate normalizes the config and fills in missing IDs.
func (c *Config) Validate() error {
	for i := range c.Sonarr {
		c.Sonarr[i].Kind = Sonarr
	}
	for i := range c.Radarr {
		c.Radarr[i].Kind = Radarr
	}
	for _, list := range [][]ArrInstance{c.Sonarr, c.Radarr} {
		for i := range list {
			a := &list[i]
			a.Name = strings.TrimSpace(a.Name)
			a.URL = strings.TrimRight(strings.TrimSpace(a.URL), "/")
			a.APIKey = strings.TrimSpace(a.APIKey)
			if a.Name == "" || a.URL == "" || a.APIKey == "" {
				return fmt.Errorf("%s instance needs a name, URL and API key", a.Kind)
			}
			if a.ID == "" {
				a.ID = newID()
			}
		}
	}
	for i := range c.Qbit {
		q := &c.Qbit[i]
		q.Name = strings.TrimSpace(q.Name)
		q.URL = strings.TrimRight(strings.TrimSpace(q.URL), "/")
		if q.Name == "" || q.URL == "" {
			return errors.New("qBittorrent instance needs a name and URL")
		}
		if q.ID == "" {
			q.ID = newID()
		}
		q.Categories = cleanList(q.Categories, false)
		var maps []PathMapping
		for _, m := range q.PathMappings {
			if strings.TrimSpace(m.Remote) == "" || strings.TrimSpace(m.Local) == "" {
				continue
			}
			maps = append(maps, PathMapping{Remote: strings.TrimSpace(m.Remote), Local: strings.TrimSpace(m.Local)})
		}
		if maps == nil {
			maps = []PathMapping{}
		}
		q.PathMappings = maps
	}
	c.ExtraLibraryPaths = cleanList(c.ExtraLibraryPaths, true)
	c.DownloadPaths = cleanList(c.DownloadPaths, true)
	c.ExcludedPaths = cleanList(c.ExcludedPaths, true)
	c.IgnoredNames = cleanList(c.IgnoredNames, false)
	for _, lists := range [][]string{c.ExtraLibraryPaths, c.DownloadPaths, c.ExcludedPaths} {
		for _, p := range lists {
			if !filepath.IsAbs(p) {
				return fmt.Errorf("path %q must be absolute", p)
			}
			if p == "/" {
				return errors.New("\"/\" is not allowed as a path")
			}
		}
	}
	if c.MinAgeHours < 0 || c.ScanIntervalHours < 0 {
		return errors.New("hours must not be negative")
	}
	return nil
}

func cleanList(in []string, paths bool) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if paths {
			s = filepath.Clean(s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Store loads and saves the config file, guarding concurrent access.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "config.json"), cfg: Default()}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, s.write()
	case err != nil:
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", s.path, err)
	}
	s.cfg = cfg
	return s, nil
}

// Get returns a deep copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var c Config
	data, _ := json.Marshal(s.cfg)
	_ = json.Unmarshal(data, &c)
	return c
}

func (s *Store) Set(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
	return s.write()
}

func (s *Store) write() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
