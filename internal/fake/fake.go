// Package fake implements just enough of the Sonarr, Radarr and qBittorrent
// APIs for tests and the demo.
package fake

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Media struct {
	ID    int
	Title string
	Year  int
	Path  string
	Files []string // absolute paths of tracked files
	// Quality is reported for every file, e.g. Bluray-1080p.
	Quality string
}

func quality(name string) map[string]any {
	return map[string]any{"quality": map[string]any{"name": name}}
}

type Arr struct {
	Kind       string // sonarr or radarr
	APIKey     string
	Roots      []string
	RecycleBin string
	Media      []Media
	// History maps an upper-case download id to a media id.
	History map[string]int
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Arr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") != a.APIKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/api/v3/") {
	case "system/status":
		name := "Radarr"
		if a.Kind == "sonarr" {
			name = "Sonarr"
		}
		writeJSON(w, map[string]string{"appName": name, "version": "5.0.0-fake"})
	case "rootfolder":
		var out []map[string]any
		for i, p := range a.Roots {
			out = append(out, map[string]any{"id": i + 1, "path": p})
		}
		writeJSON(w, out)
	case "config/mediamanagement":
		writeJSON(w, map[string]any{"recycleBin": a.RecycleBin})
	case "movie":
		var out []map[string]any
		for _, m := range a.Media {
			mv := map[string]any{"id": m.ID, "title": m.Title, "year": m.Year, "path": m.Path, "hasFile": len(m.Files) > 0}
			if len(m.Files) > 0 {
				mv["movieFile"] = map[string]any{"path": m.Files[0], "relativePath": filepath.Base(m.Files[0]), "quality": quality(m.Quality)}
			}
			out = append(out, mv)
		}
		writeJSON(w, out)
	case "series":
		var out []map[string]any
		for _, m := range a.Media {
			out = append(out, map[string]any{"id": m.ID, "title": m.Title, "year": m.Year, "path": m.Path})
		}
		writeJSON(w, out)
	case "episodefile":
		id, _ := strconv.Atoi(r.URL.Query().Get("seriesId"))
		out := []map[string]any{}
		for _, m := range a.Media {
			if m.ID == id {
				for _, f := range m.Files {
					out = append(out, map[string]any{"path": f, "quality": quality(m.Quality)})
				}
			}
		}
		writeJSON(w, out)
	case "history":
		records := []map[string]any{}
		if id, ok := a.History[r.URL.Query().Get("downloadId")]; ok {
			rec := map[string]any{"sourceTitle": "release"}
			if a.Kind == "sonarr" {
				rec["seriesId"] = id
			} else {
				rec["movieId"] = id
			}
			records = append(records, rec)
		}
		writeJSON(w, map[string]any{"records": records})
	default:
		http.NotFound(w, r)
	}
}

type Torrent struct {
	Hash     string
	Name     string
	Category string
	SavePath string
	Files    []string // relative to SavePath
	Progress float64
	AddedOn  int64
}

type Qbit struct {
	mu       sync.Mutex
	Torrents []*Torrent
	Deleted  []string
}

func (q *Qbit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch strings.TrimPrefix(r.URL.Path, "/api/v2/") {
	case "auth/login":
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "fake", Path: "/"})
		_, _ = w.Write([]byte("Ok."))
	case "app/version":
		_, _ = w.Write([]byte("v5.0.0"))
	case "torrents/info":
		var want map[string]bool
		if h := r.URL.Query().Get("hashes"); h != "" {
			want = map[string]bool{}
			for _, x := range strings.Split(h, "|") {
				want[x] = true
			}
		}
		out := []map[string]any{}
		for _, t := range q.Torrents {
			if want != nil && !want[t.Hash] {
				continue
			}
			var size int64
			for _, f := range t.Files {
				if fi, err := os.Stat(filepath.Join(t.SavePath, f)); err == nil {
					size += fi.Size()
				}
			}
			content := filepath.Join(t.SavePath, t.Files[0])
			if len(t.Files) > 1 || strings.Contains(t.Files[0], "/") {
				content = filepath.Join(t.SavePath, strings.Split(t.Files[0], "/")[0])
			}
			out = append(out, map[string]any{
				"hash": t.Hash, "name": t.Name, "category": t.Category, "save_path": t.SavePath,
				"content_path": content, "progress": t.Progress, "added_on": t.AddedOn, "size": size,
				"state": "stalledUP",
			})
		}
		writeJSON(w, out)
	case "torrents/files":
		out := []map[string]any{}
		for _, t := range q.Torrents {
			if t.Hash == r.URL.Query().Get("hash") {
				for _, f := range t.Files {
					out = append(out, map[string]any{"name": f, "progress": t.Progress})
				}
			}
		}
		writeJSON(w, out)
	case "torrents/delete":
		_ = r.ParseForm()
		del := map[string]bool{}
		for _, h := range strings.Split(r.PostForm.Get("hashes"), "|") {
			del[h] = true
		}
		kept := q.Torrents[:0]
		for _, t := range q.Torrents {
			if !del[t.Hash] {
				kept = append(kept, t)
				continue
			}
			q.Deleted = append(q.Deleted, t.Hash)
			if r.PostForm.Get("deleteFiles") == "true" {
				for _, f := range t.Files {
					_ = os.Remove(filepath.Join(t.SavePath, f))
				}
			}
		}
		q.Torrents = kept
	default:
		http.NotFound(w, r)
	}
}
