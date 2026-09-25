package cleaner

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cleanarr/internal/config"
	"cleanarr/internal/fake"
	"cleanarr/internal/scanner"
)

type env struct {
	svc     *Service
	radarr  *fake.Arr
	qb      *fake.Qbit
	movies  string
	tracked string
	orphan  string
	tfile   string
	oldTor  string
}

func setup(t *testing.T) *env {
	d := t.TempDir()
	e := &env{movies: filepath.Join(d, "movies")}
	tor := filepath.Join(d, "torrents")

	e.tracked = filepath.Join(e.movies, "Kept (2020)/Kept.mkv")
	write(t, e.tracked, 10)
	e.orphan = filepath.Join(e.movies, "Orphan (2001)/Orphan.mkv")
	write(t, e.orphan, 40)
	e.tfile = filepath.Join(tor, "movies/Orphan.2001/Orphan.mkv")
	_ = os.MkdirAll(filepath.Dir(e.tfile), 0o755)
	if err := os.Link(e.orphan, e.tfile); err != nil {
		t.Fatal(err)
	}
	e.oldTor = filepath.Join(tor, "movies/Kept.2020.720p.mkv")
	write(t, e.oldTor, 20)

	e.radarr = &fake.Arr{
		Kind: "radarr", APIKey: "k", Roots: []string{e.movies},
		Media:   []fake.Media{{ID: 1, Title: "Kept", Year: 2020, Path: filepath.Dir(e.tracked), Files: []string{e.tracked}}},
		History: map[string]int{"OLD": 1},
	}
	e.qb = &fake.Qbit{Torrents: []*fake.Torrent{
		{Hash: "orph", Name: "Orphan.2001", Category: "radarr", SavePath: filepath.Join(tor, "movies"), Files: []string{"Orphan.2001/Orphan.mkv"}, Progress: 1},
		{Hash: "old", Name: "Kept.2020.720p", Category: "radarr", SavePath: filepath.Join(tor, "movies"), Files: []string{"Kept.2020.720p.mkv"}, Progress: 1},
	}}
	rs, qs := httptest.NewServer(e.radarr), httptest.NewServer(e.qb)
	t.Cleanup(rs.Close)
	t.Cleanup(qs.Close)

	store, err := config.Open(filepath.Join(d, "config"))
	if err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	c.Radarr = []config.ArrInstance{{Name: "Radarr", URL: rs.URL, APIKey: "k", Enabled: true}}
	c.Qbit = []config.QbitInstance{{Name: "qbit", URL: qs.URL, Enabled: true}}
	c.DownloadPaths = []string{tor}
	c.MinAgeHours = 0
	if err := store.Set(c); err != nil {
		t.Fatal(err)
	}
	e.svc = New(store, filepath.Join(d, "config"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.svc.jobs.run(ctx)
	return e
}

func (e *env) scan(t *testing.T) *scanner.Result {
	t.Helper()
	if err := e.svc.StartScan(); err != nil {
		t.Fatal(err)
	}
	for e.svc.ScanState().Running {
		time.Sleep(10 * time.Millisecond)
	}
	if st := e.svc.ScanState(); st.Error != "" {
		t.Fatal(st.Error)
	}
	return e.svc.Result()
}

func (e *env) wait(t *testing.T, j *Job) *Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got := e.svc.Job(j.ID)
		if got.Status != Queued && got.Status != Running {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestDeleteOrphanWithTorrent(t *testing.T) {
	e := setup(t)
	res := e.scan(t)
	it := find(res, filepath.Dir(e.orphan))
	if it == nil {
		t.Fatalf("orphan not found")
	}
	j, err := e.svc.CreateJob([]string{it.ID}, Options{RemoveTorrents: true, RemoveLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	j = e.wait(t, j)
	if j.Status != Completed {
		t.Fatalf("status %s: %+v", j.Status, j.Log)
	}
	if exists(e.orphan) || exists(e.tfile) || exists(filepath.Dir(e.orphan)) || exists(filepath.Dir(e.tfile)) {
		t.Fatal("orphan data still exists")
	}
	if !exists(e.tracked) || !exists(e.oldTor) {
		t.Fatal("unrelated files were deleted")
	}
	if j.RemovedBytes != 40 {
		t.Fatalf("removed bytes %d, want 40", j.RemovedBytes)
	}
	if len(e.qb.Deleted) != 1 || e.qb.Deleted[0] != "orph" {
		t.Fatalf("deleted torrents %v", e.qb.Deleted)
	}
	if find(e.svc.Result(), it.Path) != nil {
		t.Fatal("deleted item still listed")
	}
}

func TestUnusedTorrentIsLabeledAndDeleted(t *testing.T) {
	e := setup(t)
	res := e.scan(t)
	it := find(res, e.oldTor)
	if it == nil || it.Category != scanner.UnusedTorrent || it.Related != "Kept (2020)" {
		t.Fatalf("unused torrent: %+v", it)
	}
	j, _ := e.svc.CreateJob([]string{it.ID}, Options{RemoveTorrents: true, RemoveLinks: true})
	if j = e.wait(t, j); j.Status != Completed || exists(e.oldTor) {
		t.Fatalf("status %s, exists %v: %+v", j.Status, exists(e.oldTor), j.Log)
	}
}

func TestImportAfterScanIsKept(t *testing.T) {
	e := setup(t)
	res := e.scan(t)
	it := find(res, filepath.Dir(e.orphan))
	// Radarr imports the "orphan" after the scan.
	e.radarr.Media = append(e.radarr.Media, fake.Media{ID: 2, Title: "Orphan", Path: filepath.Dir(e.orphan), Files: []string{e.orphan}})
	j, _ := e.svc.CreateJob([]string{it.ID}, Options{RemoveTorrents: true, RemoveLinks: true})
	j = e.wait(t, j)
	if !exists(e.orphan) {
		t.Fatalf("newly tracked file was deleted: %+v", j.Log)
	}
	if j.Status != Partial {
		t.Fatalf("status %s, want partial", j.Status)
	}
}
