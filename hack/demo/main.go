// Command demo creates a fake media library with sparse files and serves fake
// Sonarr, Radarr and qBittorrent APIs for it, so the UI can be tried locally:
//
//	go run ./hack/demo -dir /tmp/cleanarr-demo
//	go run . -config /tmp/cleanarr-demo/config
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cleanarr/internal/config"
	"cleanarr/internal/fake"
)

const gb = 1 << 30

var old = time.Now().Add(-90 * 24 * time.Hour)

func mk(path string, size float64) string {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	must(err)
	must(f.Truncate(int64(size * gb))) // sparse: takes no real disk space
	must(f.Close())
	must(os.Chtimes(path, old, old))
	return path
}

func ln(from, to string) string {
	must(os.MkdirAll(filepath.Dir(to), 0o755))
	must(os.Link(from, to))
	return to
}

// quality guesses the quality name Radarr or Sonarr would report.
func quality(file string) string {
	f := strings.ToLower(file)
	res := "1080p"
	for _, r := range []string{"2160p", "720p"} {
		if strings.Contains(f, r) {
			res = r
		}
	}
	switch {
	case strings.Contains(f, "remux"):
		return "Remux-" + res
	case strings.Contains(f, "web"):
		return "WEBDL-" + res
	}
	return "Bluray-" + res
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	dir := flag.String("dir", "/tmp/cleanarr-demo", "where to create the demo data")
	flag.Parse()
	root, err := filepath.Abs(*dir)
	must(err)
	data := filepath.Join(root, "data")
	must(os.RemoveAll(data))

	movies := filepath.Join(data, "media/movies")
	movies4k := filepath.Join(data, "media/movies-4k")
	tv := filepath.Join(data, "media/tv")
	tor := filepath.Join(data, "torrents")
	recycle := filepath.Join(data, "recycle/radarr")
	now := time.Now().Add(-60 * 24 * time.Hour).Unix()

	qb := &fake.Qbit{}
	torrent := func(hash, cat, name string, files ...string) {
		qb.Torrents = append(qb.Torrents, &fake.Torrent{Hash: hash, Name: name, Category: cat, SavePath: filepath.Join(tor, cat), Files: files, Progress: 1, AddedOn: now})
	}

	// Radarr
	radarr := &fake.Arr{Kind: "radarr", APIKey: "radarr", Roots: []string{movies}, RecycleBin: recycle, History: map[string]int{}}
	movie := func(id int, title string, year int, file string, size float64, seedHash string) string {
		dirName := fmt.Sprintf("%s (%d)", title, year)
		p := filepath.Join(movies, dirName, file)
		if seedHash != "" {
			src := mk(filepath.Join(tor, "radarr", strings.TrimSuffix(file, filepath.Ext(file)), file), size)
			ln(src, p)
			torrent(seedHash, "radarr", strings.TrimSuffix(file, filepath.Ext(file)), filepath.Join(strings.TrimSuffix(file, filepath.Ext(file)), file))
			radarr.History[strings.ToUpper(seedHash)] = id
		} else {
			mk(p, size)
		}
		mk(filepath.Join(movies, dirName, "movie.nfo"), 0)
		radarr.Media = append(radarr.Media, fake.Media{ID: id, Title: title, Year: year, Path: filepath.Join(movies, dirName), Files: []string{p}, Quality: quality(file)})
		return filepath.Join(movies, dirName)
	}
	movie(1, "Arrival", 2016, "Arrival.2016.2160p.UHD.BluRay.x265-TERMiNAL.mkv", 21.4, "a1")
	movie(2, "Blade Runner 2049", 2017, "Blade.Runner.2049.2017.1080p.BluRay.x264-SPARKS.mkv", 14.2, "")
	dune := movie(3, "Dune", 2021, "Dune.2021.2160p.WEB-DL.DDP5.1.Atmos.HDR.H.265-FLUX.mkv", 27.9, "a3")
	movie(4, "Heat", 1995, "Heat.1995.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-FGT.mkv", 32.6, "")
	movie(5, "The Thing", 1982, "The.Thing.1982.1080p.BluRay.x264-AMIABLE.mkv", 9.8, "a5")
	sicario := movie(6, "Sicario", 2015, "Sicario.2015.2160p.UHD.BluRay.x265-AAA.mkv", 18.1, "")

	// Old version of Dune left next to the upgrade.
	mk(filepath.Join(dune, "Dune.2021.1080p.WEB-DL.DDP5.1.H.264-EVO.mkv"), 8.3)
	mk(filepath.Join(dune, "Dune.2021.1080p.WEB-DL.DDP5.1.H.264-EVO.en.srt"), 0.0001)
	mk(filepath.Join(sicario, "Sample", "sicario-sample.mkv"), 0.12)
	// Previous Arrival release still seeding after the upgrade.
	mk(filepath.Join(tor, "radarr", "Arrival.2016.1080p.BluRay.x264-SPARKS", "Arrival.2016.1080p.BluRay.x264-SPARKS.mkv"), 10.9)
	torrent("b1", "radarr", "Arrival.2016.1080p.BluRay.x264-SPARKS", "Arrival.2016.1080p.BluRay.x264-SPARKS/Arrival.2016.1080p.BluRay.x264-SPARKS.mkv")
	radarr.History["B1"] = 1
	// Movies removed from Radarr but left on disk, one still seeding.
	src := mk(filepath.Join(tor, "radarr", "Cats.2019.1080p.BluRay.x264-DRONES", "Cats.2019.1080p.BluRay.x264-DRONES.mkv"), 11.2)
	ln(src, filepath.Join(movies, "Cats (2019)", "Cats.2019.1080p.BluRay.x264-DRONES.mkv"))
	torrent("c1", "radarr", "Cats.2019.1080p.BluRay.x264-DRONES", "Cats.2019.1080p.BluRay.x264-DRONES/Cats.2019.1080p.BluRay.x264-DRONES.mkv")
	mk(filepath.Join(movies, "The Room (2003)", "The.Room.2003.720p.BluRay.x264.mkv"), 4.4)
	mk(filepath.Join(movies, "The Room (2003)", "The.Room.2003.720p.BluRay.x264.nfo"), 0)
	must(os.MkdirAll(filepath.Join(movies, "Empty Folder (2010)"), 0o755))
	// Recycle bin.
	mk(filepath.Join(recycle, "Heat (1995)", "Heat.1995.720p.BluRay.x264.mkv"), 6.1)

	// Radarr 4K
	radarr4k := &fake.Arr{Kind: "radarr", APIKey: "radarr4k", Roots: []string{movies4k}, History: map[string]int{}}
	p := mk(filepath.Join(movies4k, "Dune (2021)", "Dune.2021.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-TRiToN.mkv"), 64.2)
	radarr4k.Media = append(radarr4k.Media, fake.Media{ID: 1, Title: "Dune", Year: 2021, Path: filepath.Dir(p), Files: []string{p}, Quality: quality(p)})
	mk(filepath.Join(movies4k, "Tenet (2020)", "Tenet.2020.2160p.UHD.BluRay.REMUX.HDR.HEVC-FGT.mkv"), 58.7)

	// Sonarr
	sonarr := &fake.Arr{Kind: "sonarr", APIKey: "sonarr", Roots: []string{tv}, History: map[string]int{}}
	series := func(id int, title string, year int, seasons, eps int, size float64, hashPrefix string) string {
		sdir := filepath.Join(tv, title)
		var files []string
		for s := 1; s <= seasons; s++ {
			pack := fmt.Sprintf("%s.S%02d.1080p.WEB-DL.DDP5.1.H.264-NTb", strings.ReplaceAll(title, " ", "."), s)
			var tfiles []string
			for e := 1; e <= eps; e++ {
				name := fmt.Sprintf("%s.S%02dE%02d.1080p.WEB-DL.DDP5.1.H.264-NTb.mkv", strings.ReplaceAll(title, " ", "."), s, e)
				dst := filepath.Join(sdir, fmt.Sprintf("Season %02d", s), name)
				if hashPrefix != "" {
					src := mk(filepath.Join(tor, "tv-sonarr", pack, name), size)
					ln(src, dst)
					tfiles = append(tfiles, pack+"/"+name)
				} else {
					mk(dst, size)
				}
				files = append(files, dst)
			}
			if hashPrefix != "" {
				h := fmt.Sprintf("%s%d", hashPrefix, s)
				torrent(h, "tv-sonarr", pack, tfiles...)
				sonarr.History[strings.ToUpper(h)] = id
			}
		}
		sonarr.Media = append(sonarr.Media, fake.Media{ID: id, Title: title, Year: year, Path: sdir, Files: files, Quality: "WEBDL-1080p"})
		return sdir
	}
	sev := series(1, "Severance", 2022, 2, 9, 3.1, "d")
	series(2, "The Bear", 2022, 3, 8, 1.4, "")
	series(3, "Dark", 2017, 3, 10, 2.2, "e")
	// Old episode versions after a quality upgrade.
	for e := 1; e <= 3; e++ {
		mk(filepath.Join(sev, "Season 01", fmt.Sprintf("Severance.S01E%02d.720p.WEB.h264-GGEZ.mkv", e)), 1.2)
	}
	// Series removed from Sonarr.
	for e := 1; e <= 10; e++ {
		mk(filepath.Join(tv, "Lost", "Season 01", fmt.Sprintf("Lost.S01E%02d.720p.BluRay.x264.mkv", e)), 1.1)
	}
	// Download leftovers without a torrent.
	mk(filepath.Join(tor, "tv-sonarr", "Chernobyl.S01.2160p.WEB-DL.x265", "Chernobyl.S01E01.2160p.WEB-DL.x265.mkv"), 5.8)
	mk(filepath.Join(tor, "tv-sonarr", "Chernobyl.S01.2160p.WEB-DL.x265", "Chernobyl.S01E02.2160p.WEB-DL.x265.mkv"), 5.6)
	mk(filepath.Join(tor, "radarr", "Oppenheimer.2023.1080p.WEB.part"), 3.3)

	ports := map[string]http.Handler{
		"18989": sonarr, "17878": radarr, "17879": radarr4k, "18080": qb,
	}
	cfg := config.Default()
	cfg.Sonarr = []config.ArrInstance{{Name: "Sonarr", URL: "http://127.0.0.1:18989", APIKey: "sonarr", Enabled: true}}
	cfg.Radarr = []config.ArrInstance{
		{Name: "Radarr", URL: "http://127.0.0.1:17878", APIKey: "radarr", Enabled: true},
		{Name: "Radarr 4K", URL: "http://127.0.0.1:17879", APIKey: "radarr4k", Enabled: true},
	}
	cfg.Qbit = []config.QbitInstance{{Name: "qBittorrent", URL: "http://127.0.0.1:18080", Enabled: true, Categories: []string{"radarr", "tv-sonarr"}}}
	cfg.DownloadPaths = []string{tor}
	must(cfg.Validate())
	cfgDir := filepath.Join(root, "config")
	must(os.MkdirAll(cfgDir, 0o755))
	b, _ := json.MarshalIndent(cfg, "", "  ")
	must(os.WriteFile(filepath.Join(cfgDir, "config.json"), b, 0o644))
	_ = os.Remove(filepath.Join(cfgDir, "jobs.json"))

	for port, h := range ports {
		go func() { log.Fatal(http.ListenAndServe("127.0.0.1:"+port, h)) }()
	}
	log.Printf("demo data in %s; run: go run . -config %s", data, cfgDir)
	select {}
}
