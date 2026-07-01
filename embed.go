// SRT Extractor — subtitle extraction and management for Windows.
// Copyright (C) 2026 fromdisposition
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version.
//
// This program is distributed in the hope that it will be useful, but WITHOUT
// ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License along with
// this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"compress/gzip"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
)

//go:embed assets/ffmpeg.exe.gz assets/ffprobe.exe.gz
var embedded embed.FS

var (
	embeddedDir  string
	embeddedOnce sync.Once
)

func ensureEmbedded() string {
	embeddedOnce.Do(func() { embeddedDir = extractEmbedded() })
	return embeddedDir
}

func bundleTag() string {
	h := sha1.New()
	for _, n := range []string{"assets/ffmpeg.exe.gz", "assets/ffprobe.exe.gz"} {
		if f, err := embedded.Open(n); err == nil {
			io.Copy(h, f)
			f.Close()
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func extractEmbedded() string {
	dir := filepath.Join(os.TempDir(), "srtextractor-"+bundleTag())
	os.MkdirAll(dir, 0755)
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		dst := filepath.Join(dir, name+".exe")
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			continue
		}
		if err := gunzipTo("assets/"+name+".exe.gz", dst); err != nil {
			return ""
		}
	}
	return dir
}

func gunzipTo(src, dst string) error {
	in, err := embedded.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer gz.Close()

	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, gz); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}
