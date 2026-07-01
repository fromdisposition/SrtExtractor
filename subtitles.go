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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type SubStream struct {
	Index     int
	Order     int
	Codec     string
	Lang      string
	Title     string
	Default   bool
	Forced    bool
	TextBased bool
}

func (s SubStream) Label() string {
	parts := []string{fmt.Sprintf("#%d", s.Order)}
	if s.Lang != "" {
		parts = append(parts, strings.ToUpper(s.Lang))
	}
	if s.Title != "" {
		parts = append(parts, s.Title)
	}
	parts = append(parts, s.Codec)
	if s.Default {
		parts = append(parts, "default")
	}
	if s.Forced {
		parts = append(parts, "forced")
	}
	if !s.TextBased {
		parts = append(parts, "[image — needs OCR]")
	}
	return strings.Join(parts, " · ")
}

func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

var textCodecs = map[string]bool{
	"subrip": true, "srt": true, "mov_text": true, "ass": true,
	"ssa": true, "webvtt": true, "text": true, "eia_608": true, "subviewer": true,
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func locateTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	for _, c := range []string{
		filepath.Join(dir, name+".exe"),
		filepath.Join(dir, "ffmpeg", "bin", name+".exe"),
		filepath.Join(dir, "bin", name+".exe"),
	} {
		if fileExists(c) {
			return c
		}
	}
	if d := ensureEmbedded(); d != "" {
		if p := filepath.Join(d, name+".exe"); fileExists(p) {
			return p
		}
	}
	return ""
}

type ffprobeOut struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
}

func Probe(file string) ([]SubStream, error) {
	probe := locateTool("ffprobe")
	if probe == "" {
		return nil, fmt.Errorf("ffprobe not found (put ffprobe.exe next to this program or in PATH)")
	}
	cmd := exec.Command(probe,
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "s",
		file)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffprobe: %s", msg)
	}
	var data ffprobeOut
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		return nil, err
	}
	var subs []SubStream
	order := 0
	for _, st := range data.Streams {
		if st.CodecType != "subtitle" {
			continue
		}
		s := SubStream{
			Index:     st.Index,
			Order:     order,
			Codec:     st.CodecName,
			Lang:      st.Tags["language"],
			Title:     st.Tags["title"],
			Default:   st.Disposition["default"] == 1,
			Forced:    st.Disposition["forced"] == 1,
			TextBased: textCodecs[st.CodecName],
		}
		subs = append(subs, s)
		order++
	}
	return subs, nil
}

func PreviewTrackCtx(ctx context.Context, file string, order, maxBlocks int) (string, bool, error) {
	ff := locateTool("ffmpeg")
	if ff == "" {
		return "", false, fmt.Errorf("ffmpeg not found")
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cctx, ff,
		"-v", "error",
		"-discard:v", "all", "-discard:a", "all",
		"-i", file,
		"-map", fmt.Sprintf("0:s:%d", order),
		"-f", "srt",
		"-flush_packets", "1",
		"pipe:1")
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var sb strings.Builder
	arrows, more := 0, false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "-->") {
			arrows++
			if arrows > maxBlocks {
				more = true
				break
			}
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	cancel()
	cmd.Wait()

	shown, _, _ := firstBlocks(sb.String(), maxBlocks)
	return shown, more, nil
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

func DurationSec(file string) float64 {
	probe := locateTool("ffprobe")
	if probe == "" {
		return 0
	}
	cmd := exec.Command(probe, "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", file)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var d float64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &d)
	return d
}

func tmpPath(video string) string {
	return video + ".tmp" + filepath.Ext(video)
}

func CleanupTemp(video string) {
	if p := tmpPath(video); p != video {
		os.Remove(p)
	}
}

func replaceFile(tmp, dst string) error {
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot replace original (file in use?): %v", err)
	}
	return nil
}

func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func runWithProgress(ctx context.Context, args []string, durSec float64, totalBytes int64, onPct func(float64), onLog func(string)) (string, error) {
	ff := locateTool("ffmpeg")
	if ff == "" {
		return "", fmt.Errorf("ffmpeg not found")
	}
	full := append([]string{"-nostdin", "-hide_banner", "-stats_period", "0.1", "-progress", "pipe:1"}, args...)
	cmd := exec.CommandContext(ctx, ff, full...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if onPct == nil {
				continue
			}
			line := sc.Text()
			if totalBytes > 0 {
				if v, ok := strings.CutPrefix(line, "total_size="); ok {
					var b int64
					if _, e := fmt.Sscanf(strings.TrimSpace(v), "%d", &b); e == nil {
						onPct(float64(b) / float64(totalBytes))
					}
				}
			} else if durSec > 0 {
				if v, ok := strings.CutPrefix(line, "out_time="); ok {
					if sec := parseHMS(v); sec >= 0 {
						onPct(sec / durSec)
					}
				}
			}
		}
	}()

	var tail []string
	sc := bufio.NewScanner(stderr)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		if line == "" {
			continue
		}
		if onLog != nil {
			onLog(line)
		}
		tail = append(tail, line)
		if len(tail) > 40 {
			tail = tail[len(tail)-40:]
		}
	}
	<-done
	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return strings.TrimSpace(strings.Join(tail, "\n")), fmt.Errorf("ffmpeg failed")
	}
	if onPct != nil {
		onPct(1)
	}
	return "", nil
}

func parseHMS(s string) float64 {
	s = strings.TrimSpace(s)
	var h, m int
	var sec float64
	if _, err := fmt.Sscanf(s, "%d:%d:%f", &h, &m, &sec); err != nil {
		return -1
	}
	return float64(h)*3600 + float64(m)*60 + sec
}

func ExtractToFileProgress(ctx context.Context, file string, order int, outPath string, onPct func(float64), onLog func(string)) error {
	args := []string{"-y", "-discard:v", "all", "-discard:a", "all", "-i", file, "-map", fmt.Sprintf("0:s:%d", order), "-f", "srt", outPath}
	if msg, err := runWithProgress(ctx, args, DurationSec(file), 0, onPct, onLog); err != nil {
		if msg != "" {
			return fmt.Errorf("save failed: %s", msg)
		}
		return err
	}
	return nil
}

func ExtractAllProgress(ctx context.Context, file string, outPaths map[int]string, onPct func(float64), onLog func(string)) (int, error) {
	if len(outPaths) == 0 {
		return 0, nil
	}
	args := []string{"-y", "-discard:v", "all", "-discard:a", "all", "-i", file}
	for order, p := range outPaths {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", order), "-f", "srt", p)
	}
	msg, err := runWithProgress(ctx, args, DurationSec(file), 0, onPct, onLog)

	saved := 0
	for _, p := range outPaths {
		if fi, e := os.Stat(p); e == nil && fi.Size() > 0 {
			saved++
		}
	}
	if err != nil && saved == 0 {
		if msg != "" {
			return 0, fmt.Errorf("save failed: %s", msg)
		}
		return 0, err
	}
	return saved, nil
}

func subEncoderForNew(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v", ".mov":
		return "mov_text"
	case ".webm":
		return "webvtt"
	}
	return ""
}

func ReplaceTrack(ctx context.Context, video string, order int, srtPath, lang string, onPct func(float64), onLog func(string)) error {
	ext := filepath.Ext(video)
	tmp := tmpPath(video)
	subs, _ := Probe(video)
	newIdx := len(subs) - 1
	if newIdx < 0 {
		newIdx = 0
	}
	args := []string{
		"-y", "-i", video, "-i", srtPath,
		"-map", "0",
		"-map", "-0:s:" + fmt.Sprint(order),
		"-map", "1:0",
		"-c", "copy",
	}
	if enc := subEncoderForNew(ext); enc != "" {
		args = append(args, fmt.Sprintf("-c:s:%d", newIdx), enc)
	}
	if lang != "" {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", newIdx), "language="+lang)
	}
	args = append(args, tmp)

	if msg, err := runWithProgress(ctx, args, 0, fileSize(video), onPct, onLog); err != nil {
		os.Remove(tmp)
		if msg != "" {
			return fmt.Errorf("remux failed: %s", msg)
		}
		return err
	}
	return replaceFile(tmp, video)
}

func DeleteTrack(ctx context.Context, video string, order int, onPct func(float64), onLog func(string)) error {
	tmp := tmpPath(video)
	args := []string{
		"-y", "-i", video,
		"-map", "0",
		"-map", "-0:s:" + fmt.Sprint(order),
		"-c", "copy",
		tmp,
	}
	if msg, err := runWithProgress(ctx, args, 0, fileSize(video), onPct, onLog); err != nil {
		os.Remove(tmp)
		if msg != "" {
			return fmt.Errorf("remux failed: %s", msg)
		}
		return err
	}
	return replaceFile(tmp, video)
}

func AddTrack(ctx context.Context, video, srtPath, lang, title string, onPct func(float64), onLog func(string)) error {
	ext := filepath.Ext(video)
	tmp := tmpPath(video)

	subs, _ := Probe(video)
	newIdx := len(subs)

	args := []string{
		"-y", "-i", video, "-i", srtPath,
		"-map", "0",
		"-map", "1:0",
		"-c", "copy",
	}
	if enc := subEncoderForNew(ext); enc != "" {
		args = append(args, fmt.Sprintf("-c:s:%d", newIdx), enc)
	}
	if lang != "" {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", newIdx), "language="+lang)
	}
	if title != "" {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", newIdx), "title="+title)
	}
	args = append(args, tmp)

	if msg, err := runWithProgress(ctx, args, 0, fileSize(video), onPct, onLog); err != nil {
		os.Remove(tmp)
		if msg != "" {
			return fmt.Errorf("add failed: %s", msg)
		}
		return err
	}
	return replaceFile(tmp, video)
}

func DefaultSrtName(videoPath string, s SubStream) string {
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	suffix := ""
	if s.Lang != "" {
		suffix = "." + s.Lang
	} else {
		suffix = fmt.Sprintf(".sub%d", s.Order)
	}
	return base + suffix + ".srt"
}
