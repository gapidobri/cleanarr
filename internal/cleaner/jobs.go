package cleaner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"cleanarr/internal/arr"
	"cleanarr/internal/qbit"
	"cleanarr/internal/scanner"
)

type JobStatus string

const (
	Queued      JobStatus = "queued"
	Running     JobStatus = "running"
	Completed   JobStatus = "completed"
	Partial     JobStatus = "partial" // finished with errors or skipped items
	Failed      JobStatus = "failed"
	Interrupted JobStatus = "interrupted"
)

type LogEntry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // info, warn, error
	Msg   string    `json:"msg"`
}

type Job struct {
	ID         string     `json:"id"`
	Status     JobStatus  `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  time.Time  `json:"startedAt,omitzero"`
	FinishedAt time.Time  `json:"finishedAt,omitzero"`
	Items      []PlanItem `json:"items"`
	Torrents   int        `json:"torrents"`
	Size       int64      `json:"size"`
	Freed      int64      `json:"freed"` // estimate from the plan
	Options    Options    `json:"options"`
	Step       string     `json:"step"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`

	RemovedFiles    int   `json:"removedFiles"`
	RemovedBytes    int64 `json:"removedBytes"`
	RemovedTorrents int   `json:"removedTorrents"`
	Errors          int   `json:"errors"`
	Skipped         int   `json:"skipped"`

	Log []LogEntry `json:"log"`

	plan *Plan
}

type jobQueue struct {
	svc  *Service
	path string

	mu     sync.Mutex
	jobs   []*Job
	queue  chan *Job
	active bool
}

const keepJobs = 100

func newJobQueue(svc *Service, path string) *jobQueue {
	q := &jobQueue{svc: svc, path: path, queue: make(chan *Job, 64)}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &q.jobs); err != nil {
			log.Printf("ignoring %s: %v", path, err)
		}
	}
	for _, j := range q.jobs {
		if j.Status == Queued || j.Status == Running {
			j.Status = Interrupted
			j.FinishedAt = time.Now()
		}
	}
	return q
}

func (q *jobQueue) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-q.queue:
			q.execute(ctx, j)
		}
	}
}

func (q *jobQueue) add(p *Plan) *Job {
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	j := &Job{
		ID: hex.EncodeToString(id), Status: Queued, CreatedAt: time.Now(),
		Items: p.Items, Torrents: len(p.Torrents), Size: p.Size, Freed: p.Freed, Options: p.Options,
		Log: []LogEntry{}, plan: p,
	}
	q.mu.Lock()
	q.jobs = append([]*Job{j}, q.jobs...)
	if len(q.jobs) > keepJobs {
		q.jobs = q.jobs[:keepJobs]
	}
	q.saveLocked()
	q.mu.Unlock()
	q.queue <- j
	return q.copyOf(j)
}

func (q *jobQueue) busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active || len(q.queue) > 0
}

func (q *jobQueue) copyOf(j *Job) *Job {
	c := *j
	c.Log = append([]LogEntry(nil), j.Log...)
	c.plan = nil
	return &c
}

func (q *jobQueue) list() []*Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*Job, len(q.jobs))
	for i, j := range q.jobs {
		out[i] = q.copyOf(j)
		out[i].Log = nil // the list view does not need logs
	}
	return out
}

func (q *jobQueue) get(id string) *Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID == id {
			return q.copyOf(j)
		}
	}
	return nil
}

func (q *jobQueue) saveLocked() {
	data, err := json.Marshal(q.jobs)
	if err == nil {
		tmp := q.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, q.path)
		}
	}
	if err != nil {
		log.Printf("save jobs: %v", err)
	}
}

// update mutates a job under the lock.
func (q *jobQueue) update(j *Job, fn func(j *Job)) {
	q.mu.Lock()
	fn(j)
	q.mu.Unlock()
}

func (q *jobQueue) logf(j *Job, level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("job %s: %s: %s", j.ID, level, msg)
	q.update(j, func(j *Job) {
		j.Log = append(j.Log, LogEntry{Time: time.Now(), Level: level, Msg: msg})
		switch level {
		case "error":
			j.Errors++
		case "warn":
			j.Skipped++
		}
	})
}

// safety holds a fresh view of the library, taken right before deleting.
type safety struct {
	tracked   map[string]arr.Ref
	protected map[string]bool // dirs that contain tracked files, item dirs, roots
	roots     map[string]bool
	plan      *Plan
}

var inodeOf = scanner.Inode

func (s *safety) fileOK(path string) error {
	if _, ok := s.tracked[path]; ok {
		return errors.New("file is tracked by an *arr instance")
	}
	if scanner.IsExcluded(path, s.plan.excluded) {
		return errors.New("path is excluded")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if n, ok := s.plan.nlink[path]; ok {
		_, now := inodeOf(fi)
		if now > n {
			return fmt.Errorf("file gained hardlinks since the scan (%d -> %d), rescan first", n, now)
		}
	}
	return nil
}

// linkOK is fileOK plus a check that no other known path of the same data
// became tracked since the scan. It guards hardlinks and torrent data.
func (s *safety) linkOK(path string) error {
	if err := s.fileOK(path); err != nil {
		return err
	}
	if key, ok := s.plan.idx.PathKey[path]; ok {
		for _, other := range s.plan.idx.Links[key] {
			if _, tracked := s.tracked[other]; tracked {
				return fmt.Errorf("shares data with tracked file %s", other)
			}
		}
	}
	return nil
}

func (s *safety) dirOK(path string) error {
	if s.protected[path] {
		return errors.New("folder contains tracked media or is a library root")
	}
	if scanner.IsExcluded(path, s.plan.excluded) {
		return errors.New("path is excluded")
	}
	for _, e := range s.plan.excluded {
		if scanner.Within(e, path) {
			return errors.New("folder contains an excluded path")
		}
	}
	return nil
}

func (q *jobQueue) execute(ctx context.Context, j *Job) {
	q.mu.Lock()
	q.active = true
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.active = false
		q.saveLocked()
		q.mu.Unlock()
	}()
	p := j.plan
	q.update(j, func(j *Job) {
		j.Status, j.StartedAt = Running, time.Now()
		j.Total = len(p.Torrents) + len(p.Links) + len(p.items)
		j.Step = "Verifying library"
	})

	finish := func(status JobStatus) {
		q.update(j, func(j *Job) {
			if status == Completed && (j.Errors > 0 || j.Skipped > 0) {
				status = Partial
			}
			j.Status, j.FinishedAt, j.Step = status, time.Now(), ""
			j.plan = nil
		})
	}

	// Re-read the libraries: something may have been imported since the scan.
	cfg := q.svc.cfg.Get()
	lib, err := q.svc.loadLibrary(ctx, cfg)
	if err != nil {
		q.logf(j, "error", "Could not verify the library, nothing was deleted: %v", err)
		finish(Failed)
		return
	}
	safe := &safety{tracked: lib.tracked, protected: map[string]bool{}, roots: map[string]bool{}, plan: p}
	for path := range lib.tracked {
		markAncestors(safe.protected, path)
	}
	for d := range lib.itemDirs {
		safe.protected[d] = true
		markAncestors(safe.protected, d)
	}
	for _, r := range append(append(append([]string{}, lib.roots...), lib.recycleBins...), p.roots...) {
		safe.protected[r] = true
		safe.roots[r] = true
		markAncestors(safe.protected, r)
	}
	q.logf(j, "info", "Verified against %d tracked files", len(lib.tracked))

	pruneDirs := map[string]bool{}

	// 1. Torrents.
	if len(p.Torrents) > 0 {
		q.update(j, func(j *Job) { j.Step = "Removing torrents" })
		clients := map[string]*qbit.Client{}
		for _, inst := range cfg.EnabledQbit() {
			clients[inst.ID] = qbit.New(inst)
		}
		// Check every torrent first, then remove them in batches per client:
		// waiting for qBittorrent one torrent at a time is slow.
		batches := map[*qbit.Client][]PlanTorrent{}
		for _, t := range p.Torrents {
			files := p.torrentFile[t.ID()]
			var problem error
			for _, f := range files {
				if _, err := os.Lstat(f); errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err := safe.linkOK(f); err != nil {
					problem = fmt.Errorf("%s: %w", f, err)
					break
				}
			}
			c := clients[t.ClientID]
			switch {
			case problem != nil:
				q.logf(j, "warn", "Kept torrent %q: %v", t.Name, problem)
			case c == nil:
				q.logf(j, "error", "Kept torrent %q: qBittorrent instance %s is no longer configured", t.Name, t.ClientName)
			default:
				batches[c] = append(batches[c], t)
				continue
			}
			q.update(j, func(j *Job) { j.Done++ })
		}
		for c, ts := range batches {
			for len(ts) > 0 {
				n := min(len(ts), torrentBatch)
				q.removeTorrents(ctx, j, safe, c, ts[:n], pruneDirs)
				ts = ts[n:]
			}
		}
	}

	// 2. Other hardlinks of the selected files.
	if len(p.Links) > 0 {
		q.update(j, func(j *Job) { j.Step = "Removing hardlinks" })
		for _, l := range p.Links {
			if err := safe.linkOK(l); err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					q.logf(j, "warn", "Kept hardlink %s: %v", l, err)
				}
			} else if q.removeFile(j, l) {
				q.logf(j, "info", "Removed hardlink %s", l)
			}
			pruneDirs[filepath.Dir(l)] = true
			q.update(j, func(j *Job) { j.Done++ })
		}
	}

	// 3. The selected items.
	q.update(j, func(j *Job) { j.Step = "Deleting items" })
	deleted := map[string]bool{}
	for _, it := range p.items {
		if ctx.Err() != nil {
			break
		}
		if q.deleteItem(j, safe, it, pruneDirs) {
			deleted[it.ID] = true
		}
		q.update(j, func(j *Job) { j.Done++ })
	}

	// 4. Empty folders left behind.
	q.update(j, func(j *Job) { j.Step = "Cleaning up empty folders" })
	dirs := make([]string, 0, len(pruneDirs))
	for d := range pruneDirs {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(a, b int) bool { return len(dirs[a]) > len(dirs[b]) })
	for _, d := range dirs {
		pruneUp(d, safe)
	}

	freed := freedBytes(p)
	q.update(j, func(j *Job) { j.RemovedBytes = freed })
	q.svc.forget(deleted)
	q.logf(j, "info", "Deleted %d of %d items, freeing %.1f GB", len(deleted), len(p.items), float64(freed)/(1<<30))
	finish(Completed)
}

// freedBytes counts the data of the plan that no longer has any known path.
func freedBytes(p *Plan) int64 {
	type data struct {
		size  int64
		nlink uint64
		paths map[string]bool
	}
	keys := map[scanner.Key]*data{}
	add := func(path string) {
		key, ok := p.idx.PathKey[path]
		if !ok {
			return
		}
		d := keys[key]
		if d == nil {
			d = &data{size: key.Size, nlink: p.idx.Nlink[path], paths: map[string]bool{}}
			keys[key] = d
		}
		d.paths[path] = true
	}
	for _, it := range p.items {
		for _, f := range it.Files {
			add(f.Path)
		}
	}
	for _, files := range p.torrentFile {
		for _, f := range files {
			add(f)
		}
	}
	var freed int64
	for key, d := range keys {
		for _, l := range p.idx.Links[key] {
			d.paths[l] = true
		}
		if uint64(len(d.paths)) < d.nlink {
			continue // links outside the scanned paths keep the data alive
		}
		gone := true
		for path := range d.paths {
			if _, err := os.Lstat(path); err == nil {
				gone = false
				break
			}
		}
		if gone {
			freed += d.size
		}
	}
	return freed
}

// torrentBatch caps how many hashes go into one request.
const torrentBatch = 50

// removeTorrents deletes ts from c in one request and then removes whatever
// data qBittorrent left behind.
func (q *jobQueue) removeTorrents(ctx context.Context, j *Job, safe *safety, c *qbit.Client, ts []PlanTorrent, pruneDirs map[string]bool) {
	hashes := make([]string, len(ts))
	for i, t := range ts {
		hashes[i] = t.Hash
	}
	if err := c.Delete(ctx, hashes); err != nil {
		for _, t := range ts {
			q.logf(j, "error", "Removing torrent %q failed: %v", t.Name, err)
		}
		q.update(j, func(j *Job) { j.Done += len(ts) })
		return
	}
	q.waitGone(ctx, c, hashes)
	for _, t := range ts {
		// qBittorrent deletes data asynchronously and may leave files
		// behind; remove whatever is left.
		for _, f := range safe.plan.torrentFile[t.ID()] {
			if err := safe.linkOK(f); err == nil {
				q.removeFile(j, f)
			}
			pruneDirs[filepath.Dir(f)] = true
		}
		q.update(j, func(j *Job) { j.RemovedTorrents++; j.Done++ })
		q.logf(j, "info", "Removed torrent %q from %s", t.Name, t.ClientName)
	}
}

// waitGone waits until qBittorrent no longer knows any of hashes.
func (q *jobQueue) waitGone(ctx context.Context, c *qbit.Client, hashes []string) {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		exists, err := c.Exists(ctx, hashes)
		if err == nil && len(exists) == 0 {
			// Give qBittorrent a moment to finish removing data.
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (q *jobQueue) removeFile(j *Job, path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			q.logf(j, "error", "Delete %s: %v", path, err)
		}
		return false
	}
	q.update(j, func(j *Job) { j.RemovedFiles++ })
	return true
}

func (q *jobQueue) deleteItem(j *Job, safe *safety, it *scanner.Item, pruneDirs map[string]bool) bool {
	kept := 0
	for _, f := range it.Files {
		if safe.plan.keepPaths[f.Path] {
			kept++
		}
	}
	if it.RemoveTree && kept == 0 {
		if err := safe.dirOK(it.Path); err != nil {
			q.logf(j, "warn", "Kept %s: %v", it.Path, err)
			return false
		}
		// Check every file first so a single problem keeps the whole folder.
		for _, f := range it.Files {
			if err := safe.fileOK(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				q.logf(j, "warn", "Kept %s: %s: %v", it.Path, f.Path, err)
				return false
			}
		}
		// Count what is removed before it is gone.
		for _, f := range it.Files {
			if _, err := os.Lstat(f.Path); err == nil {
				q.update(j, func(j *Job) { j.RemovedFiles++ })
			}
		}
		if err := os.RemoveAll(it.Path); err != nil {
			q.logf(j, "error", "Delete %s: %v", it.Path, err)
			return false
		}
		pruneDirs[filepath.Dir(it.Path)] = true
		q.logf(j, "info", "Deleted %s", it.Path)
		return true
	}

	ok := true
	for _, f := range it.Files {
		if safe.plan.keepPaths[f.Path] {
			continue
		}
		if err := safe.fileOK(f.Path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				q.logf(j, "warn", "Kept %s: %v", f.Path, err)
				ok = false
			}
			continue
		}
		if !q.removeFile(j, f.Path) {
			ok = false
		}
		pruneDirs[filepath.Dir(f.Path)] = true
	}
	if it.IsDir {
		pruneTree(it.Path, safe)
		pruneDirs[filepath.Dir(it.Path)] = true
	}
	if ok && kept == 0 {
		q.logf(j, "info", "Deleted %s", it.Path)
	}
	return ok && kept == 0
}

// pruneTree removes empty folders below and including dir.
func pruneTree(dir string, safe *safety) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(a, b int) bool { return len(dirs[a]) > len(dirs[b]) })
	for _, d := range dirs {
		if safe.dirOK(d) == nil {
			_ = os.Remove(d) // fails unless empty
		}
	}
}

// pruneUp removes dir and its parents while they are empty and unprotected.
// Direct children of a root (e.g. download category folders) are kept.
func pruneUp(dir string, safe *safety) {
	for {
		if safe.dirOK(dir) != nil || safe.roots[filepath.Dir(dir)] {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func markAncestors(m map[string]bool, p string) {
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return
		}
		m[parent] = true
		p = parent
	}
}
