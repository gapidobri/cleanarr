package cleaner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cleanarr/internal/arr"
	"cleanarr/internal/scanner"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(path, old, old)
}

func find(res *scanner.Result, path string) *scanner.Item {
	for _, it := range res.Items {
		if it.Path == path {
			return it
		}
	}
	return nil
}

func TestPlanRemovesLinksAndTorrents(t *testing.T) {
	d := t.TempDir()
	lib, dl, usenet := filepath.Join(d, "movies"), filepath.Join(d, "torrents"), filepath.Join(d, "usenet")
	tracked := filepath.Join(lib, "Kept (2021)/Kept.mkv")
	write(t, tracked, 10)
	orphan := filepath.Join(lib, "Orphan (2001)/Orphan.mkv")
	write(t, orphan, 40)
	tfile := filepath.Join(dl, "movies/Orphan.2001/Orphan.mkv")
	_ = os.MkdirAll(filepath.Dir(tfile), 0o755)
	if err := os.Link(orphan, tfile); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(usenet, "done/Orphan.mkv")
	_ = os.MkdirAll(filepath.Dir(extra), 0o755)
	if err := os.Link(orphan, extra); err != nil {
		t.Fatal(err)
	}

	ref := arr.Ref{Title: "Kept"}
	res := scanner.Run(scanner.Input{
		LibraryRoots:  []string{lib},
		DownloadPaths: []string{dl, usenet},
		ItemDirs:      map[string]arr.Ref{filepath.Dir(tracked): ref},
		Tracked:       map[string]arr.Ref{tracked: ref},
		Torrents: []scanner.Torrent{{
			TorrentRef:  scanner.TorrentRef{ClientID: "q", Hash: "abc", Name: "Orphan.2001"},
			ContentPath: filepath.Dir(tfile), Files: []string{tfile}, Complete: true, Allowed: true,
		}},
	})
	it := find(res, filepath.Dir(orphan))
	if it == nil {
		t.Fatalf("orphan not found in %+v", res.Items)
	}

	p, err := BuildPlan(res, []string{it.ID}, Options{RemoveTorrents: true, RemoveLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Torrents) != 1 || len(p.Links) != 1 || p.Links[0] != extra || p.Freed != 40 {
		t.Fatalf("plan: torrents=%d links=%v freed=%d", len(p.Torrents), p.Links, p.Freed)
	}

	p, _ = BuildPlan(res, []string{it.ID}, Options{})
	if len(p.Torrents) != 0 || len(p.Links) != 0 || p.Freed != 0 {
		t.Fatalf("plan without links/torrents should free nothing: %+v", p)
	}
}
