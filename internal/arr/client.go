// Package arr is a minimal client for the Sonarr and Radarr v3 APIs.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cleanarr/internal/config"
)

type Client struct {
	inst config.ArrInstance
	http *http.Client
}

func New(inst config.ArrInstance) *Client {
	return &Client{inst: inst, http: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *Client) Instance() config.ArrInstance { return c.inst }

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.inst.URL + "/api/v3/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.inst.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.inst.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: GET %s: %s %s", c.inst.Name, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", c.inst.Name, path, err)
	}
	return nil
}

type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.get(ctx, "system/status", nil, &s)
	return s, err
}

// Ref identifies the *arr item a path belongs to.
type Ref struct {
	InstanceID   string         `json:"instanceId"`
	InstanceName string         `json:"instanceName"`
	Kind         config.ArrKind `json:"kind"`
	Title        string         `json:"title"`
	Added        time.Time      `json:"added,omitzero"`
}

// Library is everything an instance tracks.
type Library struct {
	Roots      []string
	RecycleBin string
	ItemDirs   map[string]Ref    // movie / series folders
	Files      map[string]Ref    // tracked media files
	Quality    map[string]string // tracked media file -> quality name, e.g. Bluray-2160p
	TitlesByID map[int]string    // movie or series id -> title
}

// fileQuality is the quality object of a movie or episode file.
type fileQuality struct {
	Quality struct {
		Name string `json:"name"`
	} `json:"quality"`
}

func (c *Client) Library(ctx context.Context) (*Library, error) {
	lib := &Library{ItemDirs: map[string]Ref{}, Files: map[string]Ref{}, Quality: map[string]string{}, TitlesByID: map[int]string{}}

	var roots []struct {
		Path string `json:"path"`
	}
	if err := c.get(ctx, "rootfolder", nil, &roots); err != nil {
		return nil, err
	}
	for _, r := range roots {
		if r.Path != "" {
			lib.Roots = append(lib.Roots, filepath.Clean(r.Path))
		}
	}

	var mm struct {
		RecycleBin string `json:"recycleBin"`
	}
	if err := c.get(ctx, "config/mediamanagement", nil, &mm); err == nil && strings.TrimSpace(mm.RecycleBin) != "" {
		lib.RecycleBin = filepath.Clean(mm.RecycleBin)
	}

	switch c.inst.Kind {
	case config.Radarr:
		return lib, c.radarr(ctx, lib)
	case config.Sonarr:
		return lib, c.sonarr(ctx, lib)
	}
	return nil, fmt.Errorf("unknown kind %q", c.inst.Kind)
}

func (c *Client) ref(title, added string) Ref {
	r := Ref{InstanceID: c.inst.ID, InstanceName: c.inst.Name, Kind: c.inst.Kind, Title: title}
	if t, err := time.Parse(time.RFC3339, added); err == nil && t.Year() > 1970 {
		r.Added = t
	}
	return r
}

func titleYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

func (c *Client) radarr(ctx context.Context, lib *Library) error {
	var movies []struct {
		ID        int    `json:"id"`
		Title     string `json:"title"`
		Year      int    `json:"year"`
		Path      string `json:"path"`
		Added     string `json:"added"`
		HasFile   bool   `json:"hasFile"`
		MovieFile *struct {
			Path         string      `json:"path"`
			RelativePath string      `json:"relativePath"`
			Quality      fileQuality `json:"quality"`
		} `json:"movieFile"`
	}
	if err := c.get(ctx, "movie", nil, &movies); err != nil {
		return err
	}
	for _, m := range movies {
		ref := c.ref(titleYear(m.Title, m.Year), m.Added)
		lib.TitlesByID[m.ID] = ref.Title
		if m.Path != "" {
			lib.ItemDirs[filepath.Clean(m.Path)] = ref
		}
		if m.MovieFile == nil {
			continue
		}
		p := m.MovieFile.Path
		if p == "" && m.MovieFile.RelativePath != "" && m.Path != "" {
			p = filepath.Join(m.Path, m.MovieFile.RelativePath)
		}
		if p != "" {
			lib.Files[filepath.Clean(p)] = ref
			lib.Quality[filepath.Clean(p)] = m.MovieFile.Quality.Quality.Name
		}
	}
	return nil
}

func (c *Client) sonarr(ctx context.Context, lib *Library) error {
	var series []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Year  int    `json:"year"`
		Path  string `json:"path"`
		Added string `json:"added"`
	}
	if err := c.get(ctx, "series", nil, &series); err != nil {
		return err
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		sem      = make(chan struct{}, 8)
	)
	for _, s := range series {
		ref := c.ref(titleYear(s.Title, s.Year), s.Added)
		lib.TitlesByID[s.ID] = ref.Title
		if s.Path != "" {
			lib.ItemDirs[filepath.Clean(s.Path)] = ref
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id int, ref Ref) {
			defer wg.Done()
			defer func() { <-sem }()
			var files []struct {
				Path    string      `json:"path"`
				Quality fileQuality `json:"quality"`
			}
			err := c.get(ctx, "episodefile", url.Values{"seriesId": {strconv.Itoa(id)}}, &files)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for _, f := range files {
				if f.Path != "" {
					lib.Files[filepath.Clean(f.Path)] = ref
					lib.Quality[filepath.Clean(f.Path)] = f.Quality.Quality.Name
				}
			}
		}(s.ID, ref)
	}
	wg.Wait()
	return firstErr
}

// GrabbedTitle looks up which movie/series a download (torrent hash) was
// grabbed for. It returns "" when the instance has no history for it.
func (c *Client) GrabbedTitle(ctx context.Context, downloadID string, titles map[int]string) (string, error) {
	var page struct {
		Records []struct {
			MovieID     int    `json:"movieId"`
			SeriesID    int    `json:"seriesId"`
			SourceTitle string `json:"sourceTitle"`
		} `json:"records"`
	}
	q := url.Values{"downloadId": {strings.ToUpper(downloadID)}, "page": {"1"}, "pageSize": {"5"}}
	if err := c.get(ctx, "history", q, &page); err != nil {
		return "", err
	}
	for _, r := range page.Records {
		id := r.MovieID
		if c.inst.Kind == config.Sonarr {
			id = r.SeriesID
		}
		if t, ok := titles[id]; ok {
			return t, nil
		}
		if r.SourceTitle != "" {
			return r.SourceTitle + " (removed from " + c.inst.Name + ")", nil
		}
	}
	return "", nil
}
