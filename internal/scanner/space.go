package scanner

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cleanarr/internal/arr"
)

// Space describes what uses the disk space in the scanned paths. Data with
// several hardlinks is counted once, at the path that best explains why it is
// kept: a tracked file first, then the library, the recycle bin and downloads.
type Space struct {
	Size      int64          `json:"size"`     // counted once
	Apparent  int64          `json:"apparent"` // every hardlink counted
	Files     int            `json:"files"`
	Classes   []SpaceClass   `json:"classes"`
	Volumes   []VolumeUsage  `json:"volumes"`
	Titles    []TitleUsage   `json:"titles"`
	Qualities []QualityUsage `json:"qualities"`
	// Watched is true when titles carry watch history from a media server.
	Watched bool `json:"watched"`
	// WatchLoading is true while watch history is still being loaded.
	WatchLoading bool `json:"watchLoading"`

	dirs   map[string]*dirUsage
	tops   []string
	titles map[string]string // item dir -> title
}

// SpaceClass groups data by what keeps it on disk.
type SpaceClass struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // radarr, sonarr, extras, torrents, cleanup, other
	Label string `json:"label"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
	// Seeding is the part of Size a torrent holds as well, through a hardlink.
	Seeding int64 `json:"seeding"`
}

// VolumeUsage is one filesystem, identified by its total and free space.
type VolumeUsage struct {
	Paths   []string `json:"paths"`
	Total   uint64   `json:"total"`
	Free    uint64   `json:"free"`
	Scanned int64    `json:"scanned"`
	Parts   []int64  `json:"parts"` // per class, in the order of Space.Classes
}

type TitleUsage struct {
	Title    string `json:"title"`
	Instance string `json:"instance"`
	Kind     string `json:"kind"`
	Class    string `json:"class"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Files    int    `json:"files"`
	Seeding  int64  `json:"seeding"`
	// Other is what else the folder holds: extras, old versions, samples.
	Other     int64         `json:"other"`
	Qualities []QualityPart `json:"qualities"`
	Added     time.Time     `json:"added,omitzero"`
	// Watch is set when a media server knows the title.
	Watch *Watch `json:"watch,omitempty"`
}

type QualityPart struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type QualityUsage struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Files  int    `json:"files"`
	Titles int    `json:"titles"`
}

type dirUsage struct {
	size, apparent int64
	files          int
	parts          []int64
	dirs           map[string]bool
	list           []fileUsage
}

type fileUsage struct {
	name   string
	size   int64
	unique bool
	class  int
}

// DirEntry is a file or folder in a DirListing.
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	// Size counts hardlinked data once; Linked is the rest of the apparent
	// size, data that is counted at another path.
	Size   int64   `json:"size"`
	Linked int64   `json:"linked"`
	Files  int     `json:"files"`
	Parts  []int64 `json:"parts"`
	Title  string  `json:"title,omitempty"`
}

type DirListing struct {
	DirEntry
	Top     string     `json:"top"` // the scanned path that holds Path
	Entries []DirEntry `json:"entries"`
	// More entries were left out; MoreSize is their combined size.
	More     int   `json:"more"`
	MoreSize int64 `json:"moreSize"`
}

const maxDirEntries = 500

func (s *scan) space() *Space {
	sp := &Space{Watched: s.in.Activity != nil, Classes: []SpaceClass{}, Volumes: []VolumeUsage{}, Titles: []TitleUsage{}, Qualities: []QualityUsage{},
		dirs: map[string]*dirUsage{}, titles: map[string]string{}}
	for dir, ref := range s.in.ItemDirs {
		sp.titles[dir] = ref.Title
	}

	rootKind := map[string]string{}
	for _, r := range s.res.Roots {
		if r.Error == "" {
			rootKind[r.Path] = r.Kind
		}
	}
	for p := range rootKind {
		top := true
		for q := range rootKind {
			if q != p && Within(p, q) {
				top = false
			}
		}
		if top {
			sp.tops = append(sp.tops, p)
		}
	}
	sort.Strings(sp.tops)
	isTop := setOf(sp.tops...)

	// Pick the path each piece of data is counted at.
	rank := func(p string) int {
		if _, ok := s.in.Tracked[p]; ok {
			return 0
		}
		for d := filepath.Dir(p); ; d = filepath.Dir(d) {
			switch rootKind[d] {
			case "library":
				return 1
			case "recycle":
				return 2
			case "download":
				return 3
			}
			if d == filepath.Dir(d) {
				return 4
			}
		}
	}
	owner := map[Key]string{}
	ranks := map[string]int{}
	for p, f := range s.files {
		ranks[p] = rank(p)
		cur, ok := owner[f.Key]
		if !ok || ranks[p] < ranks[cur] || (ranks[p] == ranks[cur] && p < cur) {
			owner[f.Key] = p
		}
	}

	// Classes: one per *arr instance, then everything else.
	keyRef := map[Key]arr.Ref{}
	instances := map[string]arr.Ref{}
	for p, f := range s.files {
		if ref, ok := s.in.Tracked[p]; ok {
			keyRef[f.Key] = ref
			instances[ref.InstanceID] = ref
		}
	}
	var insts []arr.Ref
	for _, ref := range instances {
		insts = append(insts, ref)
	}
	sort.Slice(insts, func(i, j int) bool {
		if insts[i].Kind != insts[j].Kind {
			return insts[i].Kind < insts[j].Kind // radarr before sonarr
		}
		return insts[i].InstanceName < insts[j].InstanceName
	})
	classIdx := map[string]int{}
	addClass := func(id, kind, label string) {
		classIdx[id] = len(sp.Classes)
		sp.Classes = append(sp.Classes, SpaceClass{ID: id, Kind: kind, Label: label})
	}
	for _, ref := range insts {
		addClass("arr:"+ref.InstanceID, string(ref.Kind), ref.InstanceName)
	}
	addClass("cleanup", "cleanup", "Listed on Cleanup")
	addClass("extras", "extras", "Extras in movie and series folders")
	addClass("torrents", "torrents", "Torrents kept by Cleanarr")
	addClass("other", "other", "Other files")

	inItemDir := func(p string) bool {
		for d := filepath.Dir(p); d != filepath.Dir(d); d = filepath.Dir(d) {
			if _, ok := s.in.ItemDirs[d]; ok {
				return true
			}
			if isTop[d] {
				return false
			}
		}
		return false
	}
	classOf := func(k Key) int {
		if ref, ok := keyRef[k]; ok {
			return classIdx["arr:"+ref.InstanceID]
		}
		switch {
		case s.itemKeys[k] != nil:
			return classIdx["cleanup"]
		case len(s.idx.TorrentsByKey[k]) > 0:
			return classIdx["torrents"]
		case inItemDir(owner[k]):
			return classIdx["extras"]
		}
		return classIdx["other"]
	}

	// Titles and qualities, from the tracked copy of each piece of data.
	type titleAcc struct {
		*TitleUsage
		quality map[string]int64
	}
	titles := map[string]*titleAcc{}
	itemDirOf := map[string]string{}
	for dir, ref := range s.in.ItemDirs {
		itemDirOf[ref.InstanceID+"\x00"+ref.Title] = dir
	}
	qualities := map[string]*QualityUsage{}
	qualityTitles := map[string]map[string]bool{}

	keyClass := map[Key]int{}
	for k, p := range owner {
		c := classOf(k)
		keyClass[k] = c
		f := s.files[p]
		sp.Size += f.Size
		cl := &sp.Classes[c]
		cl.Size += f.Size
		cl.Files++
		seeding := len(s.idx.TorrentsByKey[k]) > 0
		if seeding && strings.HasPrefix(cl.ID, "arr:") {
			cl.Seeding += f.Size
		}

		ref, tracked := keyRef[k]
		if !tracked {
			continue
		}
		tk := ref.InstanceID + "\x00" + ref.Title
		t := titles[tk]
		if t == nil {
			t = &titleAcc{TitleUsage: &TitleUsage{
				Title: ref.Title, Instance: ref.InstanceName, Kind: string(ref.Kind), Class: cl.ID, Path: itemDirOf[tk],
				Added: ref.Added,
			}, quality: map[string]int64{}}
			if w, ok := s.in.Activity[t.Path]; ok {
				t.Watch = &w
			}
			titles[tk] = t
		}
		t.Size += f.Size
		t.Files++
		if seeding {
			t.Seeding += f.Size
		}
		q := s.in.Quality[p]
		if q == "" {
			q = "Unknown"
		}
		t.quality[q] += f.Size
		qu := qualities[q]
		if qu == nil {
			qu = &QualityUsage{Name: q}
			qualities[q] = qu
			qualityTitles[q] = map[string]bool{}
		}
		qu.Size += f.Size
		qu.Files++
		qualityTitles[q][tk] = true
	}

	// Folder tree below the top-level scanned paths.
	node := func(p string) *dirUsage {
		n := sp.dirs[p]
		if n == nil {
			n = &dirUsage{parts: make([]int64, len(sp.Classes)), dirs: map[string]bool{}}
			sp.dirs[p] = n
		}
		return n
	}
	for p, f := range s.files {
		sp.Apparent += f.Size
		sp.Files++
		top := ""
		for d := filepath.Dir(p); d != filepath.Dir(d); d = filepath.Dir(d) {
			if isTop[d] {
				top = d
				break
			}
		}
		if top == "" {
			continue
		}
		unique := owner[f.Key] == p
		c := keyClass[f.Key]
		dir := filepath.Dir(p)
		node(dir).list = append(node(dir).list, fileUsage{name: filepath.Base(p), size: f.Size, unique: unique, class: c})
		for d := dir; ; d = filepath.Dir(d) {
			n := node(d)
			n.apparent += f.Size
			n.files++
			if unique {
				n.size += f.Size
				n.parts[c] += f.Size
			}
			if d == top {
				break
			}
			node(filepath.Dir(d)).dirs[d] = true
		}
	}

	for _, t := range titles {
		if n := sp.dirs[t.Path]; n != nil && n.size > t.Size {
			t.Other = n.size - t.Size
		}
		for name, size := range t.quality {
			t.Qualities = append(t.Qualities, QualityPart{Name: name, Size: size})
		}
		sort.Slice(t.Qualities, func(i, j int) bool { return t.Qualities[i].Size > t.Qualities[j].Size })
		sp.Titles = append(sp.Titles, *t.TitleUsage)
	}
	sort.Slice(sp.Titles, func(i, j int) bool { return sp.Titles[i].Size > sp.Titles[j].Size })
	for name, q := range qualities {
		q.Titles = len(qualityTitles[name])
		sp.Qualities = append(sp.Qualities, *q)
	}
	sort.Slice(sp.Qualities, func(i, j int) bool { return sp.Qualities[i].Size > sp.Qualities[j].Size })

	// Volumes: scanned paths on the same filesystem report the same numbers.
	vols := map[string]*VolumeUsage{}
	var order []string
	for _, r := range s.res.Roots {
		if r.Error != "" || r.Total == 0 {
			continue
		}
		key := fmt.Sprintf("%d/%d", r.Total, r.Free)
		v := vols[key]
		if v == nil {
			v = &VolumeUsage{Total: r.Total, Free: r.Free, Parts: make([]int64, len(sp.Classes))}
			vols[key] = v
			order = append(order, key)
		}
		v.Paths = append(v.Paths, r.Path)
		if n := sp.dirs[r.Path]; n != nil && isTop[r.Path] {
			v.Scanned += n.size
			for i, x := range n.parts {
				v.Parts[i] += x
			}
		}
	}
	for _, key := range order {
		sp.Volumes = append(sp.Volumes, *vols[key])
	}
	return sp
}

func (sp *Space) entry(p string, n *dirUsage) DirEntry {
	return DirEntry{
		Name: filepath.Base(p), Path: p, IsDir: true, Size: n.size, Linked: n.apparent - n.size,
		Files: n.files, Parts: n.parts, Title: sp.titles[p],
	}
}

// Dir lists a folder, largest entries first. An empty path lists the
// top-level scanned paths.
func (sp *Space) Dir(p string) (*DirListing, bool) {
	out := &DirListing{Entries: []DirEntry{}}
	var entries []DirEntry
	if p == "" {
		out.DirEntry = DirEntry{IsDir: true, Parts: make([]int64, len(sp.Classes))}
		for _, t := range sp.tops {
			n := sp.dirs[t]
			if n == nil {
				continue
			}
			e := sp.entry(t, n)
			e.Name = t
			entries = append(entries, e)
			out.Size += n.size
			out.Linked += n.apparent - n.size
			out.Files += n.files
			for i, x := range n.parts {
				out.Parts[i] += x
			}
		}
	} else {
		p = filepath.Clean(p)
		n := sp.dirs[p]
		if n == nil {
			return nil, false
		}
		out.DirEntry = sp.entry(p, n)
		for _, t := range sp.tops {
			if Within(p, t) {
				out.Top = t
			}
		}
		for d := range n.dirs {
			entries = append(entries, sp.entry(d, sp.dirs[d]))
		}
		for _, f := range n.list {
			e := DirEntry{Name: f.name, Path: filepath.Join(p, f.name), Files: 1, Parts: make([]int64, len(sp.Classes))}
			if f.unique {
				e.Size = f.size
				e.Parts[f.class] = f.size
			} else {
				e.Linked = f.size
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		if a.Linked != b.Linked {
			return a.Linked > b.Linked
		}
		return a.Name < b.Name
	})
	if len(entries) > maxDirEntries {
		for _, e := range entries[maxDirEntries:] {
			out.More++
			out.MoreSize += e.Size
		}
		entries = entries[:maxDirEntries]
	}
	out.Entries = append(out.Entries, entries...)
	return out, true
}
