// Package cleaner ties the *arr and qBittorrent clients to the scanner and
// runs deletion jobs.
package cleaner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cleanarr/internal/arr"
	"cleanarr/internal/config"
	"cleanarr/internal/qbit"
	"cleanarr/internal/scanner"
)

type ScanState struct {
	Running   bool      `json:"running"`
	Message   string    `json:"message"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
}

type Service struct {
	cfg     *config.Store
	dataDir string

	mu     sync.RWMutex
	result *scanner.Result
	scan   ScanState

	jobs *jobQueue
}

func New(cfg *config.Store, dataDir string) *Service {
	s := &Service{cfg: cfg, dataDir: dataDir}
	s.jobs = newJobQueue(s, filepath.Join(dataDir, "jobs.json"))
	return s
}

// Run starts the job worker and the scan scheduler until ctx is done.
func (s *Service) Run(ctx context.Context) {
	go s.jobs.run(ctx)
	var last time.Time
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		hours := s.cfg.Get().ScanIntervalHours
		if hours > 0 && time.Since(last) >= time.Duration(hours)*time.Hour {
			last = time.Now()
			if err := s.StartScan(); err != nil {
				log.Printf("scheduled scan: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) Result() *scanner.Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.result
}

func (s *Service) ScanState() ScanState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scan
}

func (s *Service) setMessage(msg string) {
	s.mu.Lock()
	s.scan.Message = msg
	s.mu.Unlock()
}

func (s *Service) StartScan() error {
	s.mu.Lock()
	if s.scan.Running {
		s.mu.Unlock()
		return errors.New("a scan is already running")
	}
	if s.jobs.busy() {
		s.mu.Unlock()
		return errors.New("wait for the running deletion job to finish")
	}
	s.scan = ScanState{Running: true, Message: "Starting", StartedAt: time.Now()}
	s.mu.Unlock()

	go func() {
		res, err := s.runScan(context.Background())
		s.mu.Lock()
		defer s.mu.Unlock()
		s.scan.Running = false
		if err != nil {
			s.scan.Error = err.Error()
			s.scan.Message = "Scan failed"
			log.Printf("scan failed: %v", err)
			return
		}
		s.result = res
		s.scan.Message = fmt.Sprintf("Found %d items", len(res.Items))
		log.Printf("scan finished: %d items, %d files in %s", len(res.Items), res.FilesScanned, res.FinishedAt.Sub(res.StartedAt).Round(time.Second))
	}()
	return nil
}

// library is the union of everything the *arr instances track.
type library struct {
	roots, recycleBins []string
	itemDirs, tracked  map[string]arr.Ref
	quality            map[string]string
	clients            []*arr.Client
	titles             map[string]map[int]string // instance id -> id -> title
}

// loadLibrary queries every enabled *arr instance. Any failure is fatal:
// without the full picture tracked media would look unused.
func (s *Service) loadLibrary(ctx context.Context, cfg config.Config) (*library, error) {
	lib := &library{itemDirs: map[string]arr.Ref{}, tracked: map[string]arr.Ref{}, quality: map[string]string{}, titles: map[string]map[int]string{}}
	insts := cfg.Arrs()
	if len(insts) == 0 {
		return nil, errors.New("no Sonarr or Radarr instance configured")
	}
	type result struct {
		c   *arr.Client
		lib *arr.Library
		err error
	}
	results := make([]result, len(insts))
	var wg sync.WaitGroup
	for i, inst := range insts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := arr.New(inst)
			l, err := c.Library(ctx)
			results[i] = result{c, l, err}
		}()
	}
	wg.Wait()
	seenRoot := map[string]bool{}
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		lib.clients = append(lib.clients, r.c)
		lib.titles[r.c.Instance().ID] = r.lib.TitlesByID
		for _, root := range r.lib.Roots {
			if !seenRoot[root] {
				seenRoot[root] = true
				lib.roots = append(lib.roots, root)
			}
		}
		if cfg.IncludeRecycleBins && r.lib.RecycleBin != "" && !seenRoot[r.lib.RecycleBin] {
			seenRoot[r.lib.RecycleBin] = true
			lib.recycleBins = append(lib.recycleBins, r.lib.RecycleBin)
		}
		for k, v := range r.lib.ItemDirs {
			lib.itemDirs[k] = v
		}
		for k, v := range r.lib.Files {
			lib.tracked[k] = v
		}
		for k, v := range r.lib.Quality {
			lib.quality[k] = v
		}
	}
	for _, root := range cfg.ExtraLibraryPaths {
		if !seenRoot[root] {
			seenRoot[root] = true
			lib.roots = append(lib.roots, root)
		}
	}
	return lib, nil
}

func (s *Service) loadTorrents(ctx context.Context, cfg config.Config) ([]scanner.Torrent, map[string]*qbit.Client, error) {
	var out []scanner.Torrent
	clients := map[string]*qbit.Client{}
	for _, inst := range cfg.EnabledQbit() {
		c := qbit.New(inst)
		clients[inst.ID] = c
		all, err := c.Torrents(ctx)
		if err != nil {
			return nil, nil, err
		}
		allowed := map[string]bool{}
		for _, cat := range inst.Categories {
			allowed[cat] = true
		}
		ts := make([]scanner.Torrent, len(all))
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			firstErr error
			sem      = make(chan struct{}, 8)
		)
		for i, t := range all {
			ts[i] = scanner.Torrent{
				TorrentRef: scanner.TorrentRef{
					ClientID: inst.ID, ClientName: inst.Name, Hash: strings.ToLower(t.Hash),
					Name: t.Name, Category: t.Category, Size: t.Size,
				},
				ContentPath: c.LocalPath(t.ContentPath),
				AddedOn:     time.Unix(t.AddedOn, 0),
				Complete:    t.Progress >= 1,
				Allowed:     len(allowed) == 0 || allowed[t.Category],
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, t qbit.Torrent) {
				defer wg.Done()
				defer func() { <-sem }()
				files, err := c.Files(ctx, t.Hash)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				for _, f := range files {
					ts[i].Files = append(ts[i].Files, c.LocalPath(filepath.Join(t.SavePath, f.Name)))
				}
			}(i, t)
		}
		wg.Wait()
		if firstErr != nil {
			return nil, nil, firstErr
		}
		out = append(out, ts...)
		s.setMessage(fmt.Sprintf("Loaded %d torrents from %s", len(all), inst.Name))
	}
	return out, clients, nil
}

func (s *Service) runScan(ctx context.Context) (*scanner.Result, error) {
	cfg := s.cfg.Get()
	s.setMessage("Loading Sonarr and Radarr libraries")
	lib, err := s.loadLibrary(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.setMessage("Loading torrents")
	torrents, _, err := s.loadTorrents(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res := scanner.Run(scanner.Input{
		LibraryRoots:  lib.roots,
		RecycleBins:   lib.recycleBins,
		DownloadPaths: cfg.DownloadPaths,
		ItemDirs:      lib.itemDirs,
		Tracked:       lib.tracked,
		Quality:       lib.quality,
		Torrents:      torrents,
		Excluded:      cfg.ExcludedPaths,
		IgnoredNames:  cfg.IgnoredNames,
		MinAge:        time.Duration(cfg.MinAgeHours) * time.Hour,
		Progress:      s.setMessage,
	})
	s.setMessage("Looking up download history")
	s.labelTorrents(ctx, res, lib)
	return res, nil
}

// labelTorrents uses the *arr download history to name the movie or series
// an unused torrent was grabbed for.
func (s *Service) labelTorrents(ctx context.Context, res *scanner.Result, lib *library) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, it := range res.Items {
		if it.Category != scanner.UnusedTorrent || len(it.Torrents) == 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *scanner.Item) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, t := range it.Torrents {
				for _, c := range lib.clients {
					title, err := c.GrabbedTitle(ctx, t.Hash, lib.titles[c.Instance().ID])
					if err == nil && title != "" {
						it.Related = title
						it.Instance = c.Instance().Name
						it.Reason = "Grabbed by " + c.Instance().Name + ", no longer used by the library (likely replaced by an upgrade)"
						return
					}
				}
			}
		}(it)
	}
	wg.Wait()
}

func (s *Service) Plan(ids []string, opt Options) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return BuildPlan(s.result, ids, opt)
}

func (s *Service) CreateJob(ids []string, opt Options) (*Job, error) {
	if s.ScanState().Running {
		return nil, errors.New("wait for the scan to finish")
	}
	plan, err := s.Plan(ids, opt)
	if err != nil {
		return nil, err
	}
	return s.jobs.add(plan), nil
}

func (s *Service) Jobs() []*Job       { return s.jobs.list() }
func (s *Service) Job(id string) *Job { return s.jobs.get(id) }

// forget drops deleted items from the current results.
func (s *Service) forget(ids map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil {
		return
	}
	kept := s.result.Items[:0]
	for _, it := range s.result.Items {
		if !ids[it.ID] {
			kept = append(kept, it)
		}
	}
	s.result.Items = kept
}

// Summary is shown on the dashboard.
type Summary struct {
	Items       int                        `json:"items"`
	Size        int64                      `json:"size"`
	Reclaimable int64                      `json:"reclaimable"`
	ByCategory  map[scanner.Category]Count `json:"byCategory"`
}

type Count struct {
	Items int   `json:"items"`
	Size  int64 `json:"size"`
}

func (s *Service) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sum := Summary{ByCategory: map[scanner.Category]Count{}}
	if s.result == nil {
		return sum
	}
	for _, it := range s.result.Items {
		sum.Items++
		sum.Size += it.Size
		sum.Reclaimable += it.Reclaimable
		c := sum.ByCategory[it.Category]
		c.Items++
		c.Size += it.Size
		sum.ByCategory[it.Category] = c
	}
	return sum
}

// TestArr checks connectivity to an *arr instance.
func TestArr(ctx context.Context, inst config.ArrInstance) (string, error) {
	st, err := arr.New(inst).Status(ctx)
	if err != nil {
		return "", err
	}
	if st.AppName != "" && !strings.EqualFold(st.AppName, string(inst.Kind)) {
		return "", fmt.Errorf("this is %s, not %s", st.AppName, inst.Kind)
	}
	return fmt.Sprintf("%s %s", st.AppName, st.Version), nil
}

func TestQbit(ctx context.Context, inst config.QbitInstance) (string, error) {
	v, err := qbit.New(inst).Version(ctx)
	if err != nil {
		return "", err
	}
	return "qBittorrent " + v, nil
}
