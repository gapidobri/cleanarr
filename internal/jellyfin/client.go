// Package jellyfin is a minimal client for the Jellyfin API, used to read
// watch history.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cleanarr/internal/config"
)

type Client struct {
	inst config.JellyfinInstance
	http *http.Client
}

func New(inst config.JellyfinInstance) *Client {
	return &Client{inst: inst, http: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *Client) Instance() config.JellyfinInstance { return c.inst }

var errNotFound = errors.New("not found")

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.inst.URL + "/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", `MediaBrowser Client="Cleanarr", Token="`+c.inst.APIKey+`"`)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.inst.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: GET %s: %s %s", c.inst.Name, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", c.inst.Name, path, err)
	}
	return nil
}

type Info struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.get(ctx, "System/Info", nil, &i)
	return i, err
}

type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

func (c *Client) Users(ctx context.Context) ([]User, error) {
	var u []User
	err := c.get(ctx, "Users", nil, &u)
	return u, err
}

// Play is what one user did with one movie or episode file.
type Play struct {
	Path       string // local path
	LastPlayed time.Time
	Played     bool
	PlayCount  int
}

type item struct {
	Path     string `json:"Path"`
	UserData *struct {
		PlayCount      int    `json:"PlayCount"`
		Played         bool   `json:"Played"`
		LastPlayedDate string `json:"LastPlayedDate"`
	} `json:"UserData"`
}

// Plays lists every movie and episode with the user's play state. Paths are
// mapped to local paths.
func (c *Client) Plays(ctx context.Context, userID string) ([]Play, error) {
	const pageSize = 2000
	var out []Play
	legacy := false
	for start := 0; ; start += pageSize {
		q := url.Values{
			"Recursive":        {"true"},
			"IncludeItemTypes": {"Movie,Episode"},
			"Fields":           {"Path"},
			"EnableImages":     {"false"},
			"EnableUserData":   {"true"},
			"StartIndex":       {strconv.Itoa(start)},
			"Limit":            {strconv.Itoa(pageSize)},
		}
		var page struct {
			Items            []item `json:"Items"`
			TotalRecordCount int    `json:"TotalRecordCount"`
		}
		var err error
		if !legacy {
			q.Set("userId", userID)
			err = c.get(ctx, "Items", q, &page)
			if errors.Is(err, errNotFound) {
				legacy = true // before Jellyfin 10.9
				q.Del("userId")
			}
		}
		if legacy {
			err = c.get(ctx, "Users/"+url.PathEscape(userID)+"/Items", q, &page)
		}
		if err != nil {
			if errors.Is(err, errNotFound) {
				err = fmt.Errorf("%s: items endpoint not found", c.inst.Name)
			}
			return nil, err
		}
		for _, it := range page.Items {
			if it.Path == "" {
				continue
			}
			p := Play{Path: config.MapPath(it.Path, c.inst.PathMappings)}
			if ud := it.UserData; ud != nil {
				p.Played, p.PlayCount = ud.Played, ud.PlayCount
				p.LastPlayed = parseTime(ud.LastPlayedDate)
			}
			out = append(out, p)
		}
		if len(page.Items) < pageSize || start+pageSize >= page.TotalRecordCount {
			return out, nil
		}
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() < 1971 {
				return time.Time{}
			}
			return t
		}
	}
	return time.Time{}
}
