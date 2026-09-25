# Cleanarr

Finds media on your NAS that Sonarr and Radarr no longer use, and deletes it in the background after you review a dry run.

## What it finds

| Type | What it is |
| --- | --- |
| Untracked folders | Folders in a Sonarr/Radarr root folder that no instance knows about, e.g. a movie removed from Radarr but not from disk. |
| Untracked files | Videos inside a movie or series folder that the instance does not track. These are usually **old versions left after an upgrade**, samples, or `.part`/`.tmp` files. Subtitles and other companion files with the same name are grouped with the video. |
| Unused torrents | Completed torrents in qBittorrent whose data is not hardlinked to any tracked file. These are typically the **old release still seeding after an upgrade**. The *arr download history names the movie or series it was grabbed for. |
| Download leftovers | Files in your download paths that belong to no torrent and no tracked file. |
| Recycle bin | Contents of the Sonarr/Radarr recycle bins, if configured. |

## Space

The Space page shows what fills your disks, measured by the last scan. Data with several hardlinks, like a movie that is also seeding, is counted once, at the library copy.

- **Per disk:** how much each Sonarr/Radarr instance uses, how much is listed on Cleanup, extras (subtitles, artwork), torrents Cleanarr keeps, and what is used on the disk but **outside the scanned folders** (other shares, snapshots, `#recycle`, excluded paths).
- **Titles:** every movie and series by size, with quality, size per file, how much is also seeding, and other files in its folder. With Jellyfin connected it also shows when each title was last watched, by any user, and can list only the titles nobody watched in 3 months to 2 years.
- **Folders:** browse the scanned folders, largest first.
- **Quality:** space per quality as reported by Sonarr and Radarr, e.g. how much is Remux-2160p.

## Deleting

A file only frees space once **every hardlink** to it is gone. When deleting, Cleanarr:

1. Removes the torrents that hold the data from qBittorrent, together with their files.
2. Deletes the other hardlinks it found in the library and download paths.
3. Deletes the selected items and cleans up empty folders.

The preview shows exactly which torrents and hardlinks are affected and how much space is freed. You can turn off torrent and hardlink removal there.

Safety rules:

- Files tracked by Sonarr or Radarr are never deleted. Every job re-reads all instances right before deleting and skips anything that became tracked since the scan.
- A scan fails if any Sonarr, Radarr or qBittorrent instance cannot be reached, so an outage never makes tracked media look unused.
- Torrents are kept when they are still downloading, are in a category you did not allow, or share data with a tracked file. Files such a torrent holds are kept too.
- A file that gained a hardlink since the scan (e.g. a fresh import) is skipped.
- Anything modified within the last 24 hours (configurable) is not listed.
- Excluded paths are never listed or deleted.

## Running

Cleanarr must see the library at the **same paths** as Sonarr and Radarr. To find hardlinks it must also see your download folder. With the common `/data` layout:

```yaml
services:
  cleanarr:
    image: ghcr.io/<owner>/cleanarr:latest
    user: "1000:1000"            # same user as Sonarr/Radarr
    ports: ["9797:9797"]
    volumes:
      - ./config:/config
      - /mnt/nas/data:/data
    restart: unless-stopped
```

Open `http://<server>:9797` and, in Settings:

1. Add each Sonarr and Radarr instance (URL and API key). Root folders and recycle bins are read from them automatically.
2. Add qBittorrent. Limit it to the categories your *arr apps use (e.g. `radarr, tv-sonarr`) so other torrents are never touched. If qBittorrent sees different paths, add a path mapping like `/downloads => /data/torrents`.
3. Add your download folder (e.g. `/data/torrents`) under **Download paths**.
4. Optionally add Jellyfin (URL and an API key from Dashboard, API Keys) to see watch history on the Space page. Cleanarr only reads from it. If Jellyfin sees the library at other paths, add a path mapping like `/movies => /data/media/movies`.
5. Go to Cleanup and select **Scan now**.

There is no authentication, so only run it on a trusted network.

Every push to `master` runs the tests and publishes `ghcr.io/<owner>/cleanarr` for amd64 and arm64, tagged `latest` and with the short commit SHA. See [.github/workflows/docker.yml](.github/workflows/docker.yml).

Without Docker: `go build -o cleanarr . && ./cleanarr -config ./config -listen :9797`.

Hardlink detection needs Linux or macOS. Hardlinks are matched by inode, size and modification time rather than device number, so they are found even when the same NFS export is mounted more than once.

## Development

```sh
go test ./...
go run ./hack/demo -dir /tmp/cleanarr-demo       # fake library + fake Sonarr/Radarr/qBittorrent/Jellyfin
go run . -config /tmp/cleanarr-demo/config         # then open http://localhost:9797
```

The demo uses sparse files, so it takes almost no disk space.

The UI is plain HTML, CSS and JS in `web/`, embedded into the binary. It uses Barlow and Barlow Condensed, both under the SIL Open Font License.
