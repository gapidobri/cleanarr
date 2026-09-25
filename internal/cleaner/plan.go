package cleaner

import (
	"errors"
	"fmt"
	"sort"

	"cleanarr/internal/scanner"
)

type Options struct {
	RemoveTorrents bool `json:"removeTorrents"`
	RemoveLinks    bool `json:"removeLinks"`
}

type PlanItem struct {
	ID       string           `json:"id"`
	Category scanner.Category `json:"category"`
	Name     string           `json:"name"`
	Path     string           `json:"path"`
	Related  string           `json:"related,omitempty"`
	Size     int64            `json:"size"`
	Files    int              `json:"files"`
}

type PlanTorrent struct {
	scanner.TorrentRef
	// OtherFiles counts files in the torrent that are not part of the selection.
	OtherFiles int `json:"otherFiles"`
}

// Plan is the dry-run result: exactly what a deletion job would do.
type Plan struct {
	Items    []PlanItem    `json:"items"`
	Torrents []PlanTorrent `json:"torrents"`
	Links    []string      `json:"links"`
	Warnings []string      `json:"warnings"`
	Files    int           `json:"files"`
	Size     int64         `json:"size"`
	Freed    int64         `json:"freed"`
	Options  Options       `json:"options"`

	items       []*scanner.Item
	idx         *scanner.Index
	keepPaths   map[string]bool // item files that must stay (held by kept torrents)
	torrentFile map[string][]string
	nlink       map[string]uint64
	roots       []string
	excluded    []string
}

func BuildPlan(res *scanner.Result, ids []string, opt Options) (*Plan, error) {
	if res == nil {
		return nil, errors.New("no scan results yet, run a scan first")
	}
	if len(ids) == 0 {
		return nil, errors.New("nothing selected")
	}
	idx := res.Index
	byID := map[string]*scanner.Item{}
	for _, it := range res.Items {
		byID[it.ID] = it
	}

	p := &Plan{
		Items: []PlanItem{}, Torrents: []PlanTorrent{}, Links: []string{}, Warnings: []string{},
		Options:     opt,
		idx:         idx,
		keepPaths:   map[string]bool{},
		torrentFile: map[string][]string{},
		nlink:       idx.Nlink,
		roots:       idx.Roots,
		excluded:    idx.Excluded,
	}
	own := map[string]bool{}
	keySize := map[scanner.Key]int64{}
	keyNlink := map[scanner.Key]uint64{}
	seenID := map[string]bool{}
	for _, id := range ids {
		it := byID[id]
		if it == nil {
			return nil, fmt.Errorf("item %s not found, rescan and try again", id)
		}
		if seenID[id] {
			continue
		}
		seenID[id] = true
		p.items = append(p.items, it)
		p.Items = append(p.Items, PlanItem{ID: it.ID, Category: it.Category, Name: it.Name, Path: it.Path, Related: it.Related, Size: it.Size, Files: it.FileCount})
		for _, f := range it.Files {
			own[f.Path] = true
			keySize[f.Key] = f.Size
			keyNlink[f.Key] = f.Nlink
		}
		p.Files += it.FileCount
	}

	remove := map[string]bool{}
	blocked := map[string]string{}
	for k := range keySize {
		for _, tid := range idx.TorrentsByKey[k] {
			if why := idx.TorrentProtected(tid); why != "" {
				blocked[tid] = why
			} else if opt.RemoveTorrents {
				remove[tid] = true
			} else {
				blocked[tid] = "removing torrents is disabled"
			}
		}
	}

	links := map[string]bool{}
	for k, size := range keySize {
		p.Size += size
		free := !idx.TrackedKeys[k]
		for _, tid := range idx.TorrentsByKey[k] {
			if !remove[tid] {
				free = false
			}
		}
		known := idx.Links[k]
		for _, path := range known {
			if own[path] {
				continue
			}
			if _, inTorrent := idx.TorrentFiles[path]; inTorrent {
				continue // removed together with its torrent, or kept with it
			}
			if idx.TrackedKeys[k] || !opt.RemoveLinks || scanner.IsExcluded(path, idx.Excluded) {
				free = false
				continue
			}
			links[path] = true
		}
		if n := keyNlink[k]; n > 1 && uint64(len(known)) < n {
			free = false
		}
		if free {
			p.Freed += size
		}
	}

	// Files held by torrents that stay must not be deleted, or the torrent breaks.
	for path := range own {
		if tid, ok := idx.TorrentFiles[path]; ok && !remove[tid] {
			p.keepPaths[path] = true
		}
	}
	if n := len(p.keepPaths); n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d file(s) are kept because the torrent that holds them is kept", n))
	}

	tids := make([]string, 0, len(remove))
	for tid := range remove {
		tids = append(tids, tid)
	}
	sort.Strings(tids)
	for _, tid := range tids {
		t := idx.Torrents[tid]
		pt := PlanTorrent{TorrentRef: t.TorrentRef}
		for _, path := range t.Files {
			p.torrentFile[tid] = append(p.torrentFile[tid], path)
			if !own[path] {
				pt.OtherFiles++
			}
		}
		if pt.OtherFiles > 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Torrent %q also contains %d file(s) outside the selection; they are deleted with it", t.Name, pt.OtherFiles))
		}
		p.Torrents = append(p.Torrents, pt)
	}
	blockedIDs := make([]string, 0, len(blocked))
	for tid := range blocked {
		blockedIDs = append(blockedIDs, tid)
	}
	sort.Strings(blockedIDs)
	for _, tid := range blockedIDs {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Torrent %q is kept: %s", idx.Torrents[tid].Name, blocked[tid]))
	}

	for path := range links {
		p.Links = append(p.Links, path)
	}
	sort.Strings(p.Links)
	for _, it := range p.items {
		if it.SharesTracked {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s shares data with a tracked file; deleting it frees no space for that data", it.Name))
		}
		if it.UnknownLinks > 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s has %d hardlink(s) outside the scanned paths; add the download path in Settings to find them", it.Name, it.UnknownLinks))
		}
	}
	return p, nil
}
