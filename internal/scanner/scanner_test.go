package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cleanarr/internal/arr"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(path, old, old)
}

func link(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(from, to); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	dir   string
	in    Input
	movie string
}

// newFixture builds a library with one tracked movie, an old version next to
// it, an unknown folder, torrents and download leftovers.
func newFixture(t *testing.T) *fixture {
	d := t.TempDir()
	movies := filepath.Join(d, "media/movies")
	dl := filepath.Join(d, "torrents")
	ref := arr.Ref{InstanceName: "Radarr", Kind: "radarr", Title: "Movie (2020)"}

	// Tracked movie, hardlinked from a seeding torrent.
	cur := filepath.Join(movies, "Movie (2020)/Movie.2020.2160p.mkv")
	write(t, filepath.Join(dl, "movies/Movie.2020.2160p/Movie.2020.2160p.mkv"), 100)
	link(t, filepath.Join(dl, "movies/Movie.2020.2160p/Movie.2020.2160p.mkv"), cur)
	write(t, filepath.Join(movies, "Movie (2020)/movie.nfo"), 1)

	// Old version left in the movie folder, with a subtitle.
	write(t, filepath.Join(movies, "Movie (2020)/Movie.2020.1080p.mkv"), 50)
	write(t, filepath.Join(movies, "Movie (2020)/Movie.2020.1080p.en.srt"), 2)

	// Old release still seeding, no longer linked to the library.
	write(t, filepath.Join(dl, "movies/Movie.2020.720p.mkv"), 30)

	// Unknown movie folder, hardlinked from a torrent.
	write(t, filepath.Join(dl, "movies/Gone.1999/Gone.1999.mkv"), 40)
	link(t, filepath.Join(dl, "movies/Gone.1999/Gone.1999.mkv"), filepath.Join(movies, "Gone (1999)/Gone.1999.mkv"))

	// Leftover without a torrent.
	write(t, filepath.Join(dl, "movies/Old.Leftover/a.mkv"), 20)
	write(t, filepath.Join(dl, "movies/Old.Leftover/b.nfo"), 1)

	// Ignored names.
	write(t, filepath.Join(movies, "@eaDir/thumb.jpg"), 5)

	torrent := func(hash, name, content string, files ...string) Torrent {
		return Torrent{
			TorrentRef:  TorrentRef{ClientID: "q", ClientName: "qbit", Hash: hash, Name: name, Category: "radarr"},
			ContentPath: content, Files: files, Complete: true, Allowed: true,
			AddedOn: time.Now().Add(-72 * time.Hour),
		}
	}
	return &fixture{
		dir:   d,
		movie: cur,
		in: Input{
			LibraryRoots:  []string{movies},
			DownloadPaths: []string{dl},
			ItemDirs:      map[string]arr.Ref{filepath.Join(movies, "Movie (2020)"): ref},
			Tracked:       map[string]arr.Ref{cur: ref},
			Torrents: []Torrent{
				torrent("aaa", "Movie.2020.2160p", filepath.Join(dl, "movies/Movie.2020.2160p"), filepath.Join(dl, "movies/Movie.2020.2160p/Movie.2020.2160p.mkv")),
				torrent("bbb", "Movie.2020.720p", filepath.Join(dl, "movies/Movie.2020.720p.mkv"), filepath.Join(dl, "movies/Movie.2020.720p.mkv")),
				torrent("ccc", "Gone.1999", filepath.Join(dl, "movies/Gone.1999"), filepath.Join(dl, "movies/Gone.1999/Gone.1999.mkv")),
			},
			IgnoredNames: []string{"@eaDir"},
			MinAge:       24 * time.Hour,
		},
	}
}

func byPath(res *Result) map[string]*Item {
	m := map[string]*Item{}
	for _, it := range res.Items {
		m[it.Path] = it
	}
	return m
}

func TestScan(t *testing.T) {
	f := newFixture(t)
	res := Run(f.in)
	items := byPath(res)
	movies := f.in.LibraryRoots[0]
	dl := f.in.DownloadPaths[0]

	if len(res.Items) != 4 {
		for _, it := range res.Items {
			t.Logf("%s %s", it.Category, it.Path)
		}
		t.Fatalf("got %d items, want 4", len(res.Items))
	}

	old := items[filepath.Join(movies, "Movie (2020)/Movie.2020.1080p.mkv")]
	if old == nil || old.Category != UntrackedFile || old.Related != "Movie (2020)" {
		t.Fatalf("old version not reported correctly: %+v", old)
	}
	if old.FileCount != 2 || old.Size != 52 || old.Reclaimable != 52 {
		t.Errorf("old version: files=%d size=%d reclaimable=%d, want 2/52/52", old.FileCount, old.Size, old.Reclaimable)
	}

	gone := items[filepath.Join(movies, "Gone (1999)")]
	if gone == nil || gone.Category != UntrackedFolder || !gone.RemoveTree {
		t.Fatalf("untracked folder not reported: %+v", gone)
	}
	if len(gone.Torrents) != 1 || gone.Torrents[0].Hash != "ccc" || gone.Reclaimable != 40 {
		t.Errorf("untracked folder torrents=%v reclaimable=%d", gone.Torrents, gone.Reclaimable)
	}

	unused := items[filepath.Join(dl, "movies/Movie.2020.720p.mkv")]
	if unused == nil || unused.Category != UnusedTorrent || unused.Size != 30 {
		t.Fatalf("unused torrent not reported: %+v", unused)
	}

	left := items[filepath.Join(dl, "movies/Old.Leftover")]
	if left == nil || left.Category != DownloadLeftover || left.FileCount != 2 {
		t.Fatalf("leftover not reported: %+v", left)
	}

	// The tracked movie, its torrent and the ignored folder must never show up.
	for _, it := range res.Items {
		for _, fl := range it.Files {
			if fl.Path == f.movie || filepath.Base(filepath.Dir(fl.Path)) == "Movie.2020.2160p" || filepath.Base(filepath.Dir(fl.Path)) == "@eaDir" {
				t.Errorf("in-use file %s reported in %s", fl.Path, it.Path)
			}
		}
	}
}

func TestMinAge(t *testing.T) {
	f := newFixture(t)
	fresh := filepath.Join(f.in.LibraryRoots[0], "New (2024)/New.mkv")
	write(t, fresh, 10)
	now := time.Now()
	_ = os.Chtimes(fresh, now, now)
	if it := byPath(Run(f.in))[filepath.Dir(fresh)]; it != nil {
		t.Fatalf("recent folder reported: %+v", it)
	}
}

func TestTrackedFileOutsideItemDirProtectsFolder(t *testing.T) {
	f := newFixture(t)
	odd := filepath.Join(f.in.LibraryRoots[0], "Gone (1999)/Gone.1999.mkv")
	f.in.Tracked[odd] = arr.Ref{Title: "Gone"}
	if it := byPath(Run(f.in))[filepath.Dir(odd)]; it != nil {
		t.Fatalf("folder with tracked file reported: %+v", it)
	}
}

func TestProtectedTorrentCategory(t *testing.T) {
	f := newFixture(t)
	for i := range f.in.Torrents {
		if f.in.Torrents[i].Hash == "bbb" {
			f.in.Torrents[i].Allowed = false
		}
	}
	res := Run(f.in)
	for _, it := range res.Items {
		if it.Category == UnusedTorrent {
			t.Fatalf("torrent in unmanaged category reported: %+v", it)
		}
	}
}

func TestSpace(t *testing.T) {
	f := newFixture(t)
	f.in.Quality = map[string]string{f.movie: "Bluray-2160p"}
	sp := Run(f.in).Space
	movies := f.in.LibraryRoots[0]
	dl := f.in.DownloadPaths[0]

	// Hardlinked data is counted once: 100 tracked + 1 nfo + 52 old version
	// + 30 unused torrent + 40 untracked folder + 21 leftover.
	if sp.Size != 244 || sp.Apparent != 384 {
		t.Fatalf("size=%d apparent=%d, want 244/384", sp.Size, sp.Apparent)
	}
	class := map[string]SpaceClass{}
	for _, c := range sp.Classes {
		class[c.Kind] = c
	}
	if c := class["radarr"]; c.Size != 100 || c.Seeding != 100 {
		t.Errorf("radarr class %+v, want size 100, seeding 100", c)
	}
	if class["extras"].Size != 1 || class["cleanup"].Size != 143 || class["torrents"].Size != 0 || class["other"].Size != 0 {
		t.Errorf("classes %+v", sp.Classes)
	}

	if len(sp.Titles) != 1 {
		t.Fatalf("titles %+v", sp.Titles)
	}
	if tu := sp.Titles[0]; tu.Size != 100 || tu.Files != 1 || tu.Other != 53 || tu.Seeding != 100 || tu.Qualities[0].Name != "Bluray-2160p" {
		t.Errorf("title %+v", tu)
	}
	if len(sp.Qualities) != 1 || sp.Qualities[0].Size != 100 || sp.Qualities[0].Titles != 1 {
		t.Errorf("qualities %+v", sp.Qualities)
	}

	top, _ := sp.Dir("")
	sizes := map[string]DirEntry{}
	for _, e := range top.Entries {
		sizes[e.Path] = e
	}
	if sizes[movies].Size != 193 || sizes[dl].Size != 51 || sizes[dl].Linked != 140 || top.Size != 244 {
		t.Errorf("top level %+v", top.Entries)
	}
	d, ok := sp.Dir(filepath.Join(movies, "Movie (2020)"))
	if !ok || d.Title != "Movie (2020)" || d.Top != movies || len(d.Entries) != 4 || d.Entries[0].Size != 100 {
		t.Errorf("movie folder %+v", d)
	}
	if _, ok := sp.Dir(filepath.Join(movies, "@eaDir")); ok {
		t.Error("ignored folder listed")
	}
}
