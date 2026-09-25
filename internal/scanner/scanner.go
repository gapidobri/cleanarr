// Package scanner compares the filesystem with what the *arr apps and
// qBittorrent know about and reports media that is no longer used.
package scanner

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cleanarr/internal/arr"
)

type Category string

const (
	// A folder in a library root that no *arr instance knows about.
	UntrackedFolder Category = "untracked_folder"
	// A file that is not tracked, e.g. an old version left after an upgrade.
	UntrackedFile Category = "untracked_file"
	// Content of an *arr recycle bin.
	RecycleBin Category = "recycle_bin"
	// A completed torrent whose data is not used by any tracked file.
	UnusedTorrent Category = "unused_torrent"
	// Files in a download path that belong to no torrent and no tracked file.
	DownloadLeftover Category = "download_leftover"
)

// Key identifies file data independently of its path. Hardlinks share the
// inode, size and modification time. The device number is left out on purpose
// because the same NFS export mounted twice reports different devices.
type Key struct {
	Ino   uint64
	Size  int64
	Mtime int64
}

type File struct {
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	Nlink uint64    `json:"nlink"`
	Key   Key       `json:"-"`
	Mtime time.Time `json:"-"`
}

// TorrentRef points to a torrent in a qBittorrent instance.
type TorrentRef struct {
	ClientID   string `json:"clientId"`
	ClientName string `json:"clientName"`
	Hash       string `json:"hash"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Size       int64  `json:"size"`
}

func (t TorrentRef) ID() string { return t.ClientID + "/" + strings.ToLower(t.Hash) }

// Torrent is a torrent as provided to the scanner, with local file paths.
type Torrent struct {
	TorrentRef
	ContentPath string
	Files       []string
	AddedOn     time.Time
	Complete    bool
	// Allowed is false for torrents in categories that cleanarr must not touch.
	Allowed bool
}

type Item struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Path     string   `json:"path"`
	Root     string   `json:"root"`
	Name     string   `json:"name"`
	IsDir    bool     `json:"isDir"`
	// Related is the movie or series this item belongs (or belonged) to.
	Related  string `json:"related,omitempty"`
	Instance string `json:"instance,omitempty"`
	Reason   string `json:"reason"`

	Size        int64     `json:"size"`
	Reclaimable int64     `json:"reclaimable"`
	FileCount   int       `json:"fileCount"`
	ModTime     time.Time `json:"modTime"`

	Torrents      []TorrentRef `json:"torrents"`
	ExtraLinks    []string     `json:"extraLinks"`
	UnknownLinks  int          `json:"unknownLinks"`
	SharesTracked bool         `json:"sharesTracked"`
	Blocked       []string     `json:"blocked"`

	Files []File `json:"-"`
	// RemoveTree deletes Path recursively instead of file by file.
	RemoveTree bool `json:"-"`
}

type Root struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"` // library, recycle, download
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Error string `json:"error,omitempty"`
}

type Input struct {
	LibraryRoots  []string
	RecycleBins   []string
	DownloadPaths []string
	ItemDirs      map[string]arr.Ref
	Tracked       map[string]arr.Ref
	// Quality maps tracked files to the quality name the *arr app reports.
	Quality      map[string]string
	Torrents     []Torrent
	Excluded     []string
	IgnoredNames []string
	MinAge       time.Duration
	Now          time.Time
	// Progress, if set, receives short status messages.
	Progress func(string)
}

type Result struct {
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	Items        []*Item   `json:"items"`
	Roots        []Root    `json:"roots"`
	FilesScanned int       `json:"filesScanned"`
	TrackedFiles int       `json:"trackedFiles"`
	Torrents     int       `json:"torrents"`
	Warnings     []string  `json:"warnings"`

	Index *Index `json:"-"`
	Space *Space `json:"-"`
}

// Index is used to plan deletions after the scan.
type Index struct {
	Links         map[Key][]string // every known path of multiply-linked data
	PathKey       map[string]Key
	TrackedKeys   map[Key]bool
	Nlink         map[string]uint64
	TorrentsByKey map[Key][]string  // torrent IDs
	TorrentFiles  map[string]string // local path -> torrent ID
	Torrents      map[string]*Torrent
	TorrentKeys   map[string][]Key
	Roots         []string
	Excluded      []string
}

var videoExt = setOf(".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".mts", ".mpg", ".mpeg",
	".webm", ".iso", ".vob", ".divx", ".flv", ".ogm", ".rmvb", ".img")
var junkExt = setOf(".part", ".partial", ".!qb", ".!ut", ".tmp", ".crdownload", ".bak")

func setOf(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return m
}

func IsVideo(name string) bool { return videoExt[strings.ToLower(filepath.Ext(name))] }

type scan struct {
	in       Input
	res      *Result
	idx      *Index
	ignored  map[string]bool
	roots    map[string]bool // every root of any kind
	skeleton map[string]bool // ancestors of roots and item dirs
	files    map[string]File // every stat'ed file
	// keys of files that belong to library / recycle bin items
	itemKeys map[Key]*Item
}

func Run(in Input) *Result {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	s := &scan{
		in:  in,
		res: &Result{StartedAt: in.Now, Items: []*Item{}, Warnings: []string{}},
		idx: &Index{
			Links:         map[Key][]string{},
			PathKey:       map[string]Key{},
			TrackedKeys:   map[Key]bool{},
			Nlink:         map[string]uint64{},
			TorrentsByKey: map[Key][]string{},
			TorrentFiles:  map[string]string{},
			Torrents:      map[string]*Torrent{},
			TorrentKeys:   map[string][]Key{},
			Excluded:      in.Excluded,
		},
		ignored:  setOf(in.IgnoredNames...),
		roots:    map[string]bool{},
		skeleton: map[string]bool{},
		files:    map[string]File{},
		itemKeys: map[Key]*Item{},
	}
	s.res.Index = s.idx
	s.res.TrackedFiles = len(in.Tracked)
	s.res.Torrents = len(in.Torrents)

	for _, list := range [][]string{in.LibraryRoots, in.RecycleBins, in.DownloadPaths} {
		for _, r := range list {
			s.roots[r] = true
			s.idx.Roots = append(s.idx.Roots, r)
			addAncestors(s.skeleton, r)
		}
	}
	for d := range in.ItemDirs {
		addAncestors(s.skeleton, d)
	}

	for _, r := range in.LibraryRoots {
		s.progress("Scanning library " + r)
		if s.addRoot(r, "library") {
			s.libraryDir(r, r)
		}
	}
	for _, r := range in.RecycleBins {
		s.progress("Scanning recycle bin " + r)
		if s.addRoot(r, "recycle") {
			s.recycleBin(r)
		}
	}
	var downloads []File
	downloadRoot := map[string]string{}
	for _, r := range in.DownloadPaths {
		s.progress("Scanning downloads " + r)
		if s.addRoot(r, "download") {
			for _, f := range s.walkFiles(r) {
				downloads = append(downloads, f)
				downloadRoot[f.Path] = r
			}
		}
	}

	s.progress("Matching torrents")
	s.indexTorrents()
	for _, it := range s.res.Items {
		s.annotate(it)
	}
	s.unusedTorrents()
	s.leftovers(downloads, downloadRoot)
	s.progress("Measuring space")
	s.res.Space = s.space()

	sort.Slice(s.res.Items, func(i, j int) bool { return s.res.Items[i].Size > s.res.Items[j].Size })
	s.res.FinishedAt = time.Now()
	return s.res
}

func (s *scan) progress(msg string) {
	if s.in.Progress != nil {
		s.in.Progress(msg)
	}
}

func (s *scan) warn(format string, err error) {
	s.res.Warnings = append(s.res.Warnings, format+": "+err.Error())
}

func (s *scan) addRoot(path, kind string) bool {
	r := Root{Path: path, Kind: kind}
	fi, err := os.Stat(path)
	if err == nil && !fi.IsDir() {
		err = errors.New("not a directory")
	}
	if err != nil {
		r.Error = err.Error()
		s.res.Roots = append(s.res.Roots, r)
		s.res.Warnings = append(s.res.Warnings, kind+" path "+path+" skipped: "+err.Error())
		return false
	}
	r.Total, r.Free, _ = DiskUsage(path)
	s.res.Roots = append(s.res.Roots, r)
	return true
}

func addAncestors(m map[string]bool, p string) {
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return
		}
		m[parent] = true
		p = parent
	}
}

func (s *scan) skip(path, name string) bool {
	if s.ignored[name] {
		return true
	}
	return IsExcluded(path, s.in.Excluded)
}

// IsExcluded reports whether path is equal to or inside one of excluded.
func IsExcluded(path string, excluded []string) bool {
	for _, e := range excluded {
		if Within(path, e) {
			return true
		}
	}
	return false
}

// Within reports whether path equals dir or is inside it.
func Within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// stat records a regular file; ok is false for anything else.
func (s *scan) stat(path string) (File, bool) {
	if f, ok := s.files[path]; ok {
		return f, true
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return File{}, false
	}
	ino, nlink := Inode(fi)
	f := File{
		Path:  path,
		Size:  fi.Size(),
		Nlink: nlink,
		Mtime: fi.ModTime(),
		Key:   Key{Ino: ino, Size: fi.Size(), Mtime: fi.ModTime().UnixNano()},
	}
	s.files[path] = f
	s.idx.Nlink[path] = nlink
	s.idx.PathKey[path] = f.Key
	s.res.FilesScanned++
	if nlink > 1 {
		s.idx.Links[f.Key] = append(s.idx.Links[f.Key], path)
	}
	return f, true
}

// walkFiles returns every regular file below dir, skipping nested roots.
func (s *scan) walkFiles(dir string) []File {
	var out []File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			s.warn("read "+p, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p != dir && (s.skip(p, d.Name()) || s.roots[p]) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if f, ok := s.stat(p); ok {
				out = append(out, f)
			}
		}
		return nil
	})
	if err != nil {
		s.warn("walk "+dir, err)
	}
	return out
}

func (s *scan) readDir(dir string) []fs.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.warn("read "+dir, err)
	}
	return entries
}

// libraryDir walks the part of a library root above the movie/series folders.
func (s *scan) libraryDir(dir, root string) {
	for _, e := range s.readDir(dir) {
		p := filepath.Join(dir, e.Name())
		if s.skip(p, e.Name()) || s.roots[p] {
			continue
		}
		switch {
		case e.IsDir():
			if ref, ok := s.in.ItemDirs[p]; ok {
				s.itemDir(p, root, ref)
			} else if s.skeleton[p] {
				s.libraryDir(p, root)
			} else {
				s.untrackedFolder(p, root)
			}
		case e.Type().IsRegular():
			f, ok := s.stat(p)
			if !ok {
				continue
			}
			if _, tracked := s.in.Tracked[p]; tracked {
				s.idx.TrackedKeys[f.Key] = true
				continue
			}
			s.addItem(&Item{
				Category: UntrackedFile, Path: p, Root: root, Reason: "File in library root not tracked by any *arr instance",
				Files: []File{f},
			})
		}
	}
}

func (s *scan) untrackedFolder(p, root string) {
	files := s.walkFiles(p)
	it := &Item{
		Category: UntrackedFolder, Path: p, Root: root, IsDir: true, Files: files,
		Reason:     "Folder not known to any *arr instance",
		RemoveTree: !s.containsExcluded(p),
	}
	for _, f := range files {
		if ref, ok := s.in.Tracked[f.Path]; ok {
			// A tracked file outside of its item folder. Never delete it.
			s.idx.TrackedKeys[f.Key] = true
			s.res.Warnings = append(s.res.Warnings, "tracked file "+f.Path+" ("+ref.Title+") is outside of its item folder; "+p+" is not reported")
			return
		}
	}
	if len(files) == 0 {
		if fi, err := os.Stat(p); err == nil {
			it.ModTime = fi.ModTime()
		}
		it.Reason = "Empty folder not known to any *arr instance"
	}
	s.addItem(it)
}

func (s *scan) containsExcluded(p string) bool {
	for _, e := range s.in.Excluded {
		if Within(e, p) {
			return true
		}
	}
	return false
}

// itemDir walks a movie or series folder and reports untracked media in it.
func (s *scan) itemDir(dir, root string, ref arr.Ref) {
	var untracked []File
	for _, f := range s.walkFiles(dir) {
		if _, ok := s.in.Tracked[f.Path]; ok {
			s.idx.TrackedKeys[f.Key] = true
			continue
		}
		untracked = append(untracked, f)
	}

	// Group companion files (subtitles, nfo, ...) with the video they belong to.
	byDir := map[string][]File{}
	for _, f := range untracked {
		byDir[filepath.Dir(f.Path)] = append(byDir[filepath.Dir(f.Path)], f)
	}
	for _, files := range byDir {
		used := map[string]bool{}
		for _, v := range files {
			name := filepath.Base(v.Path)
			if !IsVideo(name) {
				continue
			}
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			it := &Item{
				Category: UntrackedFile, Path: v.Path, Root: root, Related: ref.Title, Instance: ref.InstanceName,
				Reason: "Video not tracked by " + ref.InstanceName + " (old version or extra)",
				Files:  []File{v},
			}
			if strings.Contains(strings.ToLower(stem), "sample") {
				it.Reason = "Sample video not tracked by " + ref.InstanceName
			}
			used[v.Path] = true
			for _, c := range files {
				cn := filepath.Base(c.Path)
				if !used[c.Path] && !IsVideo(cn) && strings.HasPrefix(cn, stem+".") {
					it.Files = append(it.Files, c)
					used[c.Path] = true
				}
			}
			s.addItem(it)
		}
		for _, f := range files {
			if !used[f.Path] && junkExt[strings.ToLower(filepath.Ext(f.Path))] {
				s.addItem(&Item{
					Category: UntrackedFile, Path: f.Path, Root: root, Related: ref.Title, Instance: ref.InstanceName,
					Reason: "Incomplete or temporary file", Files: []File{f},
				})
			}
		}
	}
}

func (s *scan) recycleBin(root string) {
	for _, e := range s.readDir(root) {
		p := filepath.Join(root, e.Name())
		if s.skip(p, e.Name()) || s.roots[p] {
			continue
		}
		it := &Item{Category: RecycleBin, Path: p, Root: root, Reason: "In *arr recycle bin"}
		if e.IsDir() {
			it.IsDir = true
			it.RemoveTree = !s.containsExcluded(p)
			it.Files = s.walkFiles(p)
		} else if f, ok := s.stat(p); ok {
			it.Files = []File{f}
		} else {
			continue
		}
		s.addItem(it)
	}
}

func (s *scan) addItem(it *Item) {
	it.Name = filepath.Base(it.Path)
	it.ID = itemID(string(it.Category), it.Path)
	it.Torrents, it.ExtraLinks, it.Blocked = []TorrentRef{}, []string{}, []string{}
	seen := map[Key]bool{}
	for _, f := range it.Files {
		if f.Mtime.After(it.ModTime) {
			it.ModTime = f.Mtime
		}
		if !seen[f.Key] {
			seen[f.Key] = true
			it.Size += f.Size
		}
	}
	it.FileCount = len(it.Files)
	if it.Category != RecycleBin && s.in.MinAge > 0 && s.in.Now.Sub(it.ModTime) < s.in.MinAge {
		return
	}
	for _, f := range it.Files {
		if s.itemKeys[f.Key] == nil {
			s.itemKeys[f.Key] = it
		}
	}
	s.res.Items = append(s.res.Items, it)
}

func itemID(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

func (s *scan) indexTorrents() {
	for i := range s.in.Torrents {
		t := &s.in.Torrents[i]
		id := t.ID()
		s.idx.Torrents[id] = t
		for _, p := range t.Files {
			s.idx.TorrentFiles[p] = id
			f, ok := s.stat(p)
			if !ok {
				continue
			}
			s.idx.TorrentsByKey[f.Key] = appendUnique(s.idx.TorrentsByKey[f.Key], id)
			s.idx.TorrentKeys[id] = append(s.idx.TorrentKeys[id], f.Key)
		}
	}
}

// TorrentProtected explains why a torrent must not be removed, or returns "".
func (idx *Index) TorrentProtected(id string) string {
	t := idx.Torrents[id]
	switch {
	case t == nil:
		return "unknown torrent"
	case !t.Allowed:
		return "category \"" + t.Category + "\" is not managed by cleanarr"
	case !t.Complete:
		return "still downloading"
	}
	for _, k := range idx.TorrentKeys[id] {
		if idx.TrackedKeys[k] {
			return "contains files used by the library"
		}
	}
	return ""
}

// annotate fills in hardlink and torrent information of an item.
func (s *scan) annotate(it *Item) {
	own := map[string]bool{}
	for _, f := range it.Files {
		own[f.Path] = true
	}
	torrents := map[string]bool{}
	links := map[string]bool{}
	blocked := map[string]bool{}
	seen := map[Key]bool{}
	it.Reclaimable = 0
	it.UnknownLinks = 0
	for _, f := range it.Files {
		if seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		free := true
		if s.idx.TrackedKeys[f.Key] {
			it.SharesTracked = true
			free = false
		}
		known := s.idx.Links[f.Key]
		if f.Nlink > 1 && uint64(len(known)) < f.Nlink {
			it.UnknownLinks += int(f.Nlink) - len(known)
			free = false
		}
		for _, tid := range s.idx.TorrentsByKey[f.Key] {
			if why := s.idx.TorrentProtected(tid); why != "" {
				blocked[s.idx.Torrents[tid].Name+": "+why] = true
				free = false
			} else {
				torrents[tid] = true
			}
		}
		for _, p := range known {
			if !own[p] {
				if _, inTorrent := s.idx.TorrentFiles[p]; !inTorrent {
					links[p] = true
				}
			}
		}
		if free {
			it.Reclaimable += f.Size
		}
	}
	it.Torrents = it.Torrents[:0]
	for tid := range torrents {
		it.Torrents = append(it.Torrents, s.idx.Torrents[tid].TorrentRef)
	}
	it.ExtraLinks = sortedKeys(links)
	it.Blocked = sortedKeys(blocked)
	sort.Slice(it.Torrents, func(i, j int) bool { return it.Torrents[i].Name < it.Torrents[j].Name })
}

// unusedTorrents reports completed torrents whose data is not used by the
// library, e.g. the old release after an upgrade.
func (s *scan) unusedTorrents() {
	claimed := map[Key]*Item{}
	ids := make([]string, 0, len(s.idx.Torrents))
	for id := range s.idx.Torrents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var added []*Item
	for _, id := range ids {
		t := s.idx.Torrents[id]
		keys := s.idx.TorrentKeys[id]
		if !t.Allowed || !t.Complete || len(keys) == 0 || s.idx.TorrentProtected(id) != "" {
			continue
		}
		if s.in.MinAge > 0 && s.in.Now.Sub(t.AddedOn) < s.in.MinAge {
			continue
		}
		var owner *Item
		inLibraryItem := false
		for _, k := range keys {
			if s.itemKeys[k] != nil {
				inLibraryItem = true
			}
			if claimed[k] != nil {
				owner = claimed[k]
			}
		}
		if inLibraryItem {
			continue // removed together with the library item it is linked to
		}
		if owner == nil {
			owner = &Item{
				Category: UnusedTorrent, Path: t.ContentPath, Root: t.ClientName, Name: t.Name,
				Reason: "Torrent data is not used by any tracked file",
				IsDir:  len(t.Files) > 1,
			}
			owner.ID = itemID(string(UnusedTorrent), id)
			added = append(added, owner)
		}
		have := map[string]bool{}
		for _, f := range owner.Files {
			have[f.Path] = true
		}
		for _, p := range t.Files {
			if f, ok := s.files[p]; ok && !have[p] {
				owner.Files = append(owner.Files, f)
			}
		}
		for _, k := range keys {
			claimed[k] = owner
		}
	}
	for _, it := range added {
		name, id := it.Name, it.ID
		s.addItem(it)
		it.Name, it.ID = name, id
		// addItem skips recent items; only annotate the ones that were kept.
		if len(s.res.Items) > 0 && s.res.Items[len(s.res.Items)-1] == it {
			s.annotate(it)
		}
	}
}

// leftovers reports download files that no torrent and no tracked file use.
func (s *scan) leftovers(files []File, rootOf map[string]string) {
	// Folders that hold torrents (save paths and their parents) are structure,
	// not content.
	structure := map[string]bool{}
	for _, t := range s.idx.Torrents {
		for _, p := range t.Files {
			addAncestors(structure, p)
		}
		delete(structure, t.ContentPath)
	}
	for _, r := range s.in.DownloadPaths {
		addAncestors(structure, r)
		structure[r] = true
	}

	groups := map[string]*Item{}
	var order []string
	for _, f := range files {
		if _, ok := s.idx.TorrentFiles[f.Path]; ok {
			continue
		}
		if s.idx.TrackedKeys[f.Key] || s.itemKeys[f.Key] != nil || len(s.idx.TorrentsByKey[f.Key]) > 0 {
			continue
		}
		root := rootOf[f.Path]
		// The group is the topmost folder below the download structure.
		group := f.Path
		for d := filepath.Dir(f.Path); !structure[d] && Within(d, root) && d != root; d = filepath.Dir(d) {
			group = d
		}
		it := groups[group]
		if it == nil {
			it = &Item{
				Category: DownloadLeftover, Path: group, Root: root, IsDir: group != f.Path,
				Reason: "Download not used by any torrent or tracked file",
			}
			groups[group] = it
			order = append(order, group)
		}
		it.Files = append(it.Files, f)
	}
	for _, g := range order {
		it := groups[g]
		s.addItem(it)
		if len(s.res.Items) > 0 && s.res.Items[len(s.res.Items)-1] == it {
			s.annotate(it)
		}
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
