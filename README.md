# SRT Extractor

> Fast, portable subtitle extraction and management for Windows — a single `.exe`, no installation, ffmpeg built in.

**SRT Extractor** is a small native Windows tool for working with the subtitle tracks inside video files. It lists every subtitle track in a video, previews and exports them to `.srt`, and lets you **replace, add, or delete** subtitle tracks in place — all through a simple GUI, without re-encoding the video.

![Platform](https://img.shields.io/badge/platform-Windows%20x64-0078D6)
![Language](https://img.shields.io/badge/language-Go-00ADD8)
![ffmpeg](https://img.shields.io/badge/powered%20by-ffmpeg-007808)
![Dependencies](https://img.shields.io/badge/install-none%20(portable)-success)
![License](https://img.shields.io/badge/license-GPLv3-blue)

---

## Features

- 🎬 **Reads any container** — MKV, MP4, MOV, WebM, AVI, TS, M4V (anything ffmpeg can demux).
- 📋 **Lists all subtitle tracks** with language, title, codec, and default/forced flags.
- 👁️ **Instant preview** — shows the first ~30 subtitle blocks of a track on demand; reads only the *start* of the file, so it's fast even on huge videos.
- 💾 **Export** — save the selected track, or all text tracks at once, to `.srt`.
- ➕ **Add a track** — mux an external `.srt` into the video with a title and language.
- ♻️ **Replace a track** — swap a subtitle track for an external `.srt`.
- 🗑️ **Delete a track** — remove a subtitle track from the video.
- 📈 **Live progress** — a smooth, real progress bar plus a live ffmpeg log panel.
- 📦 **Truly portable** — ffmpeg and ffprobe are embedded inside the executable; nothing to install.

Editing operations (add / replace / delete) **stream-copy** the video and audio (`-c copy`), so they are fast and lossless, and the original file is replaced **atomically** — it can never be left half-written or corrupted.

---

## Download & run

1. Grab `SrtExtractor.exe` from the [**latest release**](../../releases/latest).
2. Double-click it. That's it — no setup, no dependencies.

### Where ffmpeg comes from

The program locates `ffmpeg`/`ffprobe` in this order:

1. **A system install** on your `PATH` (e.g. from `winget install ffmpeg`) — used as-is.
2. **Next to the exe** — `ffmpeg.exe`/`ffprobe.exe` in the same folder (or `./ffmpeg/bin`, `./bin`).
3. **Bundled fallback** — a copy embedded in the exe, unpacked to a per-user temp folder **only if
   the first two fail** (once, then cached).

So if you already have ffmpeg installed, the program uses it and **never writes anything to temp**;
the bundle is just a convenience so the tool works out-of-the-box on a clean machine.

### Verifying the bundled ffmpeg is genuine 🔒

The embedded `ffmpeg`/`ffprobe` are the **unmodified official binaries** from
[gyan.dev](https://www.gyan.dev/ffmpeg/builds/) — they are **not** rebuilt or patched by me, only
gzip-compressed. Anyone can prove this independently.

**Bundled build:** `ffmpeg 8.1.2-essentials_build-www.gyan.dev` — GPLv3 (`--enable-gpl --enable-version3`, incl. libx264/libx265)

| File | SHA-256 |
|------|---------|
| `ffmpeg.exe` (extracted) | `1326DDE4C84FF1F96FE6B8916C5BED29E163E9B5DCCF995F6F3DB069D143EC5E` |
| `ffprobe.exe` (extracted) | `B49CCC7C6547B141AD5A2F6EC69CC04323D7133D7704D70B331B904C63EECB07` |
| `assets/ffmpeg.exe.gz` | `72D93DF0CFD84C08CE0C917705CCE468CD7769924FDEC79E177890498EA0ED11` |
| `assets/ffprobe.exe.gz` | `C518593E5D9B34E6A46FBE9AD08F9E6270786D13E5F8BDC7436E7C529ECCBC0B` |

To check (values are also in [`CHECKSUMS.txt`](CHECKSUMS.txt)):

```powershell
# after running the app once, ffmpeg is in %TEMP%\srtextractor-<hash>\
Get-FileHash "$env:TEMP\srtextractor-*\ffmpeg.exe" -Algorithm SHA256
```

Compare with the table above — and for a fully independent check, download the same version from
gyan.dev, extract it, and confirm the hash matches. It will, because these are the **same original
binaries**.

**Open a video** by any of:
- Drag & drop it onto the window,
- Click **Browse…**,
- Right-click a video in Explorer → *Open with* → `SrtExtractor.exe`.

---

## How it works

| Operation | ffmpeg strategy | Notes |
|---|---|---|
| **Preview** | `-map 0:s:N -f srt`, killed after 30 blocks | Reads only the beginning of the file. |
| **Export** | `-discard:v/-discard:a -map 0:s -f srt` | Skips video/audio at the demuxer, reads mostly subtitle data. |
| **Add / Replace / Delete** | `-map 0 [...] -c copy` → atomic replace | No re-encode; rewrites the container once. |

A few deliberate design choices:

- **Existing tracks are preserved.** When adding/replacing, only the *new* subtitle stream is (re)encoded for the container (`-c:s:<idx> mov_text` for MP4, `webvtt` for WebM, copy for MKV). Existing ASS keeps its styling and image subtitles (PGS/DVD) are never broken.
- **Fonts, chapters and metadata survive** the remux (`-map 0` keeps attachment streams).
- **Atomic overwrite** — the new file is renamed over the original via `MoveFileEx(REPLACE_EXISTING)`; there is no moment where the file is missing.

> **Image-based subtitles** (PGS/`hdmv_pgs_subtitle`, DVD/`dvd_subtitle`) cannot be converted to text `.srt` without OCR, so they're marked in the list. You can still **Replace** their slot with an external `.srt`.

---

## Building from source

**Requirements**
- **Go** matching `go.mod` (the module targets `go 1.26`; `1.21+` is the minimum for `//go:embed`).
- The `assets/` folder with `ffmpeg.exe.gz` and `ffprobe.exe.gz` (these get embedded into the exe).

The build has **two steps**: first generate the resource object (`.syso`) that embeds the
application manifest + icon, then compile.

> The Common Controls v6 manifest is **required** — without it the GUI toolkit fails to
> initialize. It's baked into the exe, so the result is fully self-contained.
> There is **no CGo** — the whole thing is pure Go, so you can even build the Windows binary
> **from Linux or macOS** by cross-compiling.

### Windows (PowerShell)

```powershell
go run ./cmd/mkmanifest srtextractor.exe.manifest icon.png rsrc_windows_amd64.syso
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-H windowsgui -s -w" -o SrtExtractor.exe .
```

### Linux / macOS (cross-compile to Windows)

```bash
go run ./cmd/mkmanifest srtextractor.exe.manifest icon.png rsrc_windows_amd64.syso

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-H windowsgui -s -w" -o SrtExtractor.exe .
```

The flags: `-H windowsgui` hides the console window, `-s -w` strips debug info for a smaller
binary, `-trimpath` removes local paths from the build.

### Continuous integration

Every push to `main` is built automatically by GitHub Actions
([`.github/workflows/build.yml`](.github/workflows/build.yml)) — it runs `go vet`, produces
`SrtExtractor.exe`, and uploads it as a build artifact. On `main` it also moves the rolling
`latest` tag to that commit and refreshes the single [**Latest**](../../releases/latest) release,
so the Releases page always holds exactly the newest binary — nothing older.

---

## Project structure

```
.
├── main.go                     # GUI (lxn/walk): layout, event handlers, progress/log
├── subtitles.go                # ffmpeg/ffprobe logic: probe, extract, add/replace/delete
├── embed.go                    # embeds & unpacks the bundled ffmpeg/ffprobe
├── cmd/mkmanifest/             # generates the .syso (manifest + icon), pure Go, no C
├── assets/
│   ├── ffmpeg.exe.gz           # embedded, gzip-compressed
│   └── ffprobe.exe.gz
├── icon.png                    # application icon (embedded into the exe)
├── srtextractor.exe.manifest   # Common Controls v6 + DPI manifest (embedded)
├── .github/workflows/build.yml # CI: build & release
└── LICENSE                     # GNU GPL v3
```

---

## Tech stack

- **Go** with `//go:embed` for the bundled binaries.
- **[lxn/walk](https://github.com/lxn/walk)** + **[lxn/win](https://github.com/lxn/win)** for the native Windows GUI.
- **[ffmpeg](https://ffmpeg.org/)** / **ffprobe** for all media work.

No CGo, no C compiler — a single `go build` produces the whole thing.

---

## Notes & limitations

- **Windows x64 only.**
- Editing a subtitle track means the container is rewritten once (a temp file in the same folder, then an atomic rename). This is inherent to container formats — you cannot insert/remove a track "in place".
- Text subtitle codecs (`subrip`, `mov_text`, `ass`, `webvtt`, …) export cleanly to `.srt`; image subtitles need OCR (not included).

---

## Credits

Powered by **[FFmpeg](https://ffmpeg.org/)**. The bundled `ffmpeg`/`ffprobe` are the
`gyan.dev` GPL build (configured with `--enable-gpl --enable-version3`, incl. libx264/libx265),
licensed under the **GNU GPL v3**. FFmpeg is a trademark of Fabrice Bellard.

## License

This program is free software licensed under the **GNU General Public License v3.0** — see
[`LICENSE`](LICENSE). You may use, study, share, and modify it; any distributed derivative must
remain under the GPL (source included). It comes with **no warranty**.

Copyright © 2026 fromdisposition
