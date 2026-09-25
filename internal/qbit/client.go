// Package qbit is a minimal client for the qBittorrent Web API v2.
package qbit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"cleanarr/internal/config"
)

type Client struct {
	inst config.QbitInstance
	http *http.Client

	mu       sync.Mutex
	loggedIn bool
}

func New(inst config.QbitInstance) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{inst: inst, http: &http.Client{Timeout: time.Minute, Jar: jar}}
}

func (c *Client) Instance() config.QbitInstance { return c.inst }

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.inst.Username}, "password": {c.inst.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.inst.URL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.inst.URL)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.inst.Name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// Older versions answer 200 "Ok.", newer ones 204 with an empty body.
	if !ok(resp.StatusCode) || strings.TrimSpace(string(body)) == "Fails." {
		return fmt.Errorf("%s: login failed: %s %s", c.inst.Name, resp.Status, strings.TrimSpace(string(body)))
	}
	c.loggedIn = true
	return nil
}

func ok(code int) bool { return code >= 200 && code < 300 }

func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	c.mu.Lock()
	if !c.loggedIn {
		if err := c.login(ctx); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()

	for attempt := 0; ; attempt++ {
		var body io.Reader
		u := c.inst.URL + "/api/v2/" + path
		if method == http.MethodGet && len(form) > 0 {
			u += "?" + form.Encode()
		} else if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return err
		}
		req.Header.Set("Referer", c.inst.URL)
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", c.inst.Name, err)
		}
		// The session expired: 403 on older versions, 401 on newer ones.
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && attempt == 0 {
			resp.Body.Close()
			c.mu.Lock()
			err := c.login(ctx)
			c.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		defer resp.Body.Close()
		if !ok(resp.StatusCode) {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("%s: %s %s: %s %s", c.inst.Name, method, path, resp.Status, strings.TrimSpace(string(msg)))
		}
		if out == nil {
			return nil
		}
		if s, ok := out.(*string); ok {
			b, err := io.ReadAll(resp.Body)
			*s = strings.TrimSpace(string(b))
			return err
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v string
	err := c.do(ctx, http.MethodGet, "app/version", nil, &v)
	return v, err
}

type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Progress    float64 `json:"progress"`
	AddedOn     int64   `json:"added_on"`
	Size        int64   `json:"size"`
	State       string  `json:"state"`
	Ratio       float64 `json:"ratio"`
}

type File struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

// Torrents lists torrents, restricted to the configured categories.
func (c *Client) Torrents(ctx context.Context) ([]Torrent, error) {
	var all []Torrent
	if err := c.do(ctx, http.MethodGet, "torrents/info", nil, &all); err != nil {
		return nil, err
	}
	if len(c.inst.Categories) == 0 {
		return all, nil
	}
	allowed := map[string]bool{}
	for _, cat := range c.inst.Categories {
		allowed[cat] = true
	}
	var out []Torrent
	for _, t := range all {
		if allowed[t.Category] {
			out = append(out, t)
		}
	}
	return out, nil
}

func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	var files []File
	err := c.do(ctx, http.MethodGet, "torrents/files", url.Values{"hash": {hash}}, &files)
	return files, err
}

// Delete removes torrents together with their data.
func (c *Client) Delete(ctx context.Context, hashes []string) error {
	form := url.Values{"hashes": {strings.Join(hashes, "|")}, "deleteFiles": {"true"}}
	return c.do(ctx, http.MethodPost, "torrents/delete", form, nil)
}

// Exists reports which of the given hashes are still known to qBittorrent.
func (c *Client) Exists(ctx context.Context, hashes []string) (map[string]bool, error) {
	var ts []Torrent
	if err := c.do(ctx, http.MethodGet, "torrents/info", url.Values{"hashes": {strings.Join(hashes, "|")}}, &ts); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range ts {
		out[strings.ToLower(t.Hash)] = true
	}
	return out, nil
}

// LocalPath maps a path as seen by qBittorrent to the local filesystem.
func (c *Client) LocalPath(p string) string { return config.MapPath(p, c.inst.PathMappings) }
