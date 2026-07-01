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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procSetClassLongPtr = user32.NewProc("SetClassLongPtrW")
	procGetClassLongPtr = user32.NewProc("GetClassLongPtrW")
)

func enableFullRedraw(hwnd win.HWND) {
	const gclStyle = ^uintptr(25)
	const csVRedraw = 0x0001
	const csHRedraw = 0x0002
	cur, _, _ := procGetClassLongPtr.Call(uintptr(hwnd), gclStyle)
	procSetClassLongPtr.Call(uintptr(hwnd), gclStyle, cur|csHRedraw|csVRedraw)
}

func removeTitleIcon(hwnd win.HWND) {
	const gclpHIcon = ^uintptr(13)
	const gclpHIconSm = ^uintptr(33)
	win.SendMessage(hwnd, win.WM_SETICON, 0, 0)
	win.SendMessage(hwnd, win.WM_SETICON, 1, 0)
	procSetClassLongPtr.Call(uintptr(hwnd), gclpHIcon, 0)
	procSetClassLongPtr.Call(uintptr(hwnd), gclpHIconSm, 0)
	ex := win.GetWindowLong(hwnd, win.GWL_EXSTYLE)
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, ex|win.WS_EX_DLGMODALFRAME)
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED)
}

const previewBlocks = 30

type subModel struct {
	walk.ListModelBase
	items []SubStream
}

func (m *subModel) ItemCount() int          { return len(m.items) }
func (m *subModel) Value(i int) interface{} { return m.items[i].Label() }

type App struct {
	mw         *walk.MainWindow
	pathEdit   *walk.LineEdit
	list       *walk.ListBox
	preview    *walk.TextEdit
	status     *walk.StatusBarItem
	previewBtn *walk.PushButton
	saveBtn    *walk.PushButton
	saveAllBtn *walk.PushButton
	replaceBtn *walk.PushButton
	addBtn     *walk.PushButton
	deleteBtn  *walk.PushButton
	progress   *walk.ProgressBar
	logBox     *walk.TextEdit
	logLines   []string
	progTarget atomic.Int32
	progStop   chan struct{}

	curFile string
	model   *subModel

	previewCache map[int]string
	previewMore  map[int]bool
	loadSeq      int

	previewCancel context.CancelFunc
	previewWG     sync.WaitGroup
	extracting    bool
	busy          bool
}

func (a *App) cancelPreview() {
	if a.previewCancel != nil {
		a.previewCancel()
		a.previewWG.Wait()
		a.previewCancel = nil
	}
	a.extracting = false
}

func (a *App) setBusy(b bool, durKnown bool) {
	a.busy = b
	a.stopProgress()
	if b && a.logBox != nil {
		a.logLines = nil
		a.logBox.SetText("")
	}
	if a.progress != nil {
		if !b {
			a.progress.SetMarqueeMode(false)
			a.progress.SetValue(0)
		} else if durKnown {
			a.progTarget.Store(0)
			a.progress.SetMarqueeMode(false)
			a.progress.SetRange(0, 100)
			a.progress.SetValue(0)
			a.startProgress()
		} else {
			a.progress.SetMarqueeMode(true)
		}
	}
	a.updateButtons()
}

func (a *App) startProgress() {
	stop := make(chan struct{})
	a.progStop = stop
	go func() {
		t := time.NewTicker(25 * time.Millisecond)
		defer t.Stop()
		shown := 0
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				target := int(a.progTarget.Load())
				if shown == target {
					continue
				}
				shown = easeStep(shown, target)
				v := shown
				a.mw.Synchronize(func() {
					if a.progress != nil {
						a.progress.SetValue(v)
					}
				})
			}
		}
	}()
}

func easeStep(shown, target int) int {
	if shown == target {
		return shown
	}
	diff := target - shown
	step := diff / 4
	if step == 0 {
		if diff > 0 {
			step = 1
		} else {
			step = -1
		}
	}
	return shown + step
}

func (a *App) stopProgress() {
	if a.progStop != nil {
		close(a.progStop)
		a.progStop = nil
	}
}

func (a *App) onPct() func(float64) {
	return func(p float64) {
		v := int(p * 100)
		if v < 0 {
			v = 0
		} else if v > 100 {
			v = 100
		}
		a.progTarget.Store(int32(v))
	}
}

func (a *App) onLog() func(string) {
	return func(line string) {
		a.mw.Synchronize(func() {
			a.logLines = append(a.logLines, line)
			if len(a.logLines) > 200 {
				a.logLines = a.logLines[len(a.logLines)-200:]
			}
			a.logBox.SetText(strings.Join(a.logLines, "\r\n"))
			a.logBox.SendMessage(0x0115, 7, 0)
		})
	}
}

func (a *App) curSub() (SubStream, bool) {
	i := a.list.CurrentIndex()
	if i < 0 || i >= len(a.model.items) {
		return SubStream{}, false
	}
	return a.model.items[i], true
}

func (a *App) updateButtons() {
	if a.busy {
		a.list.SetEnabled(false)
		a.previewBtn.SetEnabled(false)
		a.saveBtn.SetEnabled(false)
		a.saveAllBtn.SetEnabled(false)
		a.replaceBtn.SetEnabled(false)
		a.addBtn.SetEnabled(false)
		a.deleteBtn.SetEnabled(false)
		return
	}
	a.list.SetEnabled(true)
	a.addBtn.SetEnabled(a.curFile != "")
	a.saveAllBtn.SetEnabled(len(a.model.items) > 0)

	s, ok := a.curSub()
	a.replaceBtn.SetEnabled(ok)
	a.deleteBtn.SetEnabled(ok)
	if !ok || !s.TextBased {
		a.previewBtn.SetEnabled(false)
		a.saveBtn.SetEnabled(false)
		return
	}

	a.saveBtn.SetEnabled(true)

	_, shown := a.previewCache[s.Order]
	a.previewBtn.SetEnabled(!shown && !a.extracting)
}

func main() {
	app := &App{model: &subModel{}}

	if err := app.mainWindow().Create(); err != nil {
		walk.MsgBox(nil, "Error", err.Error(), walk.MsgBoxIconError)
		return
	}
	app.setup()

	if len(os.Args) > 1 {
		app.load(os.Args[1])
	}
	app.mw.Run()
}

func (a *App) mainWindow() MainWindow {
	return MainWindow{
		AssignTo: &a.mw,
		Title:    "SRT Extractor — subtitle extraction and management",
		MinSize:  Size{Width: 820, Height: 560},
		Size:     Size{Width: 980, Height: 640},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 10},
		OnDropFiles: func(files []string) {
			if len(files) > 0 {
				a.load(files[0])
			}
		},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "File:"},
					LineEdit{AssignTo: &a.pathEdit, ReadOnly: true, Text: "Drag a video here or click 'Browse…'"},
					PushButton{Text: "Browse…", MinSize: Size{Width: 90}, OnClicked: a.browse},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 10},
				Children: []Widget{
					GroupBox{
						Title:   "Subtitle tracks",
						Layout:  VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}},
						MinSize: Size{Width: 300},
						MaxSize: Size{Width: 320},

						Children: []Widget{ListBox{AssignTo: &a.list, Model: a.model}},
					},
					GroupBox{
						Title:  "Preview (.srt)",
						Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}},
						Children: []Widget{
							TextEdit{AssignTo: &a.preview, ReadOnly: true, VScroll: true, HScroll: true,
								Font: Font{Family: "Consolas", PointSize: 10}},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					PushButton{AssignTo: &a.previewBtn, Text: "Show preview", MinSize: Size{Width: 130}, Enabled: false, OnClicked: a.showPreview},
					PushButton{AssignTo: &a.replaceBtn, Text: "Replace…", MinSize: Size{Width: 130}, Enabled: false, OnClicked: a.replaceTrack},
					PushButton{AssignTo: &a.addBtn, Text: "Add track…", MinSize: Size{Width: 130}, Enabled: false, OnClicked: a.addTrack},
					PushButton{AssignTo: &a.deleteBtn, Text: "Delete track", MinSize: Size{Width: 130}, Enabled: false, OnClicked: a.deleteTrack},
					HSpacer{},
					PushButton{AssignTo: &a.saveBtn, Text: "Save selected…", MinSize: Size{Width: 140}, Enabled: false, OnClicked: a.saveCurrent},
					PushButton{AssignTo: &a.saveAllBtn, Text: "Save all", MinSize: Size{Width: 140}, Enabled: false, OnClicked: a.saveAll},
				},
			},
			GroupBox{
				Title:   "ffmpeg log",
				Layout:  VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}},
				MaxSize: Size{Height: 120},
				Children: []Widget{
					TextEdit{AssignTo: &a.logBox, ReadOnly: true, VScroll: true,
						Font: Font{Family: "Consolas", PointSize: 8}},
				},
			},
			ProgressBar{AssignTo: &a.progress, MarqueeMode: false, MaxSize: Size{Height: 18}},
		},
		StatusBarItems: []StatusBarItem{{AssignTo: &a.status, Text: toolStatus(), Width: 900}},
	}
}

func (a *App) setup() {
	if ico, err := walk.NewIconFromResourceId(1); err == nil {
		a.mw.SetIcon(ico)
	}
	enableFullRedraw(a.mw.Handle())
	a.list.MouseUp().Attach(func(x, y int, button walk.MouseButton) { a.onSelect() })
	a.list.KeyUp().Attach(func(key walk.Key) { a.onSelect() })
	a.updateButtons()
}

func toolStatus() string {
	ff := locateTool("ffmpeg")
	fp := locateTool("ffprobe")
	if ff == "" || fp == "" {
		return "⚠ ffmpeg/ffprobe not found — put ffmpeg.exe and ffprobe.exe next to this program"
	}
	return "Ready. ffmpeg: " + ff
}

func (a *App) setStatus(s string) {
	if a.status != nil {
		a.status.SetText(s)
	}
}

func (a *App) browse() {
	dlg := new(walk.FileDialog)
	dlg.Title = "Select a video file"
	dlg.Filter = "Video (*.mkv;*.mp4;*.mov;*.avi;*.webm;*.ts;*.m4v)|*.mkv;*.mp4;*.mov;*.avi;*.webm;*.ts;*.m4v|All files (*.*)|*.*"
	if ok, err := dlg.ShowOpen(a.mw); err != nil || !ok {
		return
	}
	a.load(dlg.FilePath)
}

func (a *App) load(file string) {
	a.cancelPreview()

	a.loadSeq++
	a.previewCache = map[int]string{}
	a.previewMore = map[int]bool{}
	a.curFile = file
	CleanupTemp(file)
	a.pathEdit.SetText(file)
	a.preview.SetText("")

	a.setStatus("Analyzing file…")

	subs, err := Probe(file)
	if err != nil {
		a.model.items = nil
		a.model.PublishItemsReset()
		a.updateButtons()
		a.setStatus("Error: " + err.Error())
		return
	}

	a.model.items = subs
	a.model.PublishItemsReset()

	if len(subs) == 0 {
		a.updateButtons()
		a.setStatus("No subtitles found in the file. You can still add a new track.")
		return
	}

	a.list.SetCurrentIndex(0)
	a.preview.SetText("Select a track and click 'Show preview'.")
	a.updateButtons()
	a.setStatus(fmt.Sprintf("Ready. Tracks found: %d. Select a track and click 'Show preview'.", len(subs)))
}

func (a *App) onSelect() {
	if a.busy || a.extracting {
		return
	}
	s, ok := a.curSub()
	if !ok {
		return
	}
	if !s.TextBased {
		a.preview.SetText("This track is image-based subtitles (" + s.Codec + ").\r\nDirect conversion to .srt is impossible without OCR.\r\n(But its slot can be 'Replaced' with an external .srt.)")
	} else if _, shown := a.previewCache[s.Order]; shown {
		a.showPreviewText(s)
	} else {
		a.preview.SetText("Click 'Show preview' to extract the beginning of this track's subtitles.")
		a.setStatus(fmt.Sprintf("Track #%d selected. Click 'Show preview'.", s.Order))
	}
	a.updateButtons()
}

func (a *App) showPreview() {
	if a.busy || a.extracting {
		return
	}
	s, ok := a.curSub()
	if !ok || !s.TextBased {
		return
	}
	if _, shown := a.previewCache[s.Order]; shown {
		a.showPreviewText(s)
		a.updateButtons()
		return
	}
	a.startPreview()
}

func (a *App) startPreview() {
	s, ok := a.curSub()
	if !ok || !s.TextBased {
		return
	}
	video, order, seq := a.curFile, s.Order, a.loadSeq

	a.extracting = true
	a.preview.SetText("Extracting the start of the track…")
	a.setStatus(fmt.Sprintf("Extracting the first %d blocks…", previewBlocks))
	a.updateButtons()

	ctx, cancel := context.WithCancel(context.Background())
	a.previewCancel = cancel
	a.previewWG.Add(1)
	go func() {
		defer a.previewWG.Done()
		text, more, err := PreviewTrackCtx(ctx, video, order, previewBlocks)
		a.mw.Synchronize(func() {
			a.extracting = false
			a.previewCancel = nil
			if ctx.Err() != nil || seq != a.loadSeq {
				return
			}
			if err != nil {
				a.preview.SetText("Extraction error: " + err.Error())
				a.updateButtons()
				return
			}
			a.previewCache[order] = text
			a.previewMore[order] = more
			if cur, ok := a.curSub(); ok && cur.Order == order {
				a.showPreviewText(cur)
			}
			a.updateButtons()
		})
	}()
}

func firstBlocks(srt string, n int) (shown string, shownN, total int) {
	norm := strings.ReplaceAll(srt, "\r\n", "\n")
	total = strings.Count(norm, "-->")
	blocks := strings.Split(strings.Trim(norm, "\n"), "\n\n")
	if len(blocks) <= n {
		return norm, len(blocks), total
	}
	return strings.Join(blocks[:n], "\n\n"), n, total
}

func (a *App) showPreviewText(s SubStream) {
	text := a.previewCache[s.Order]
	disp := strings.ReplaceAll(text, "\r\n", "\n")
	if a.previewMore[s.Order] {
		disp = fmt.Sprintf("▼ Preview: first %d blocks. Full text via 'Save'. ▼\n\n%s",
			previewBlocks, disp)
	}
	a.preview.SetText(strings.ReplaceAll(disp, "\n", "\r\n"))
	a.setStatus(fmt.Sprintf("Track #%d · preview shown.", s.Order))
}

func (a *App) saveCurrent() {
	i := a.list.CurrentIndex()
	if i < 0 || i >= len(a.model.items) {
		return
	}
	s := a.model.items[i]

	dlg := new(walk.FileDialog)
	dlg.Title = "Save SRT"
	dlg.Filter = "SRT subtitles (*.srt)|*.srt"
	dlg.FilePath = DefaultSrtName(a.curFile, s)
	if ok, err := dlg.ShowSave(a.mw); err != nil || !ok {
		return
	}

	path := dlg.FilePath
	if !strings.HasSuffix(strings.ToLower(path), ".srt") {
		path += ".srt"
	}

	video, order := a.curFile, s.Order
	onPct, onLog := a.onPct(), a.onLog()
	a.setBusy(true, true)
	a.setStatus("Saving full .srt…")
	go func() {

		err := ExtractToFileProgress(context.Background(), video, order, path, onPct, onLog)
		a.mw.Synchronize(func() {
			a.setBusy(false, false)
			if err != nil {
				a.setStatus("Error: " + err.Error())
				walk.MsgBox(a.mw, "Error", err.Error(), walk.MsgBoxIconError)
				return
			}
			a.setStatus("Saved: " + path)
		})
	}()
}

func (a *App) replaceTrack() {
	i := a.list.CurrentIndex()
	if i < 0 || i >= len(a.model.items) {
		return
	}
	s := a.model.items[i]

	dlg := new(walk.FileDialog)
	dlg.Title = "Select a .srt to mux into the video"
	dlg.Filter = "SRT subtitles (*.srt)|*.srt|All files (*.*)|*.*"
	if ok, err := dlg.ShowOpen(a.mw); err != nil || !ok {
		return
	}

	srt := dlg.FilePath
	if walk.MsgBox(a.mw, "Confirm",
		fmt.Sprintf("Replace track #%d (%s %s) with:\n%s\n\nThe video file will be overwritten. Continue?",
			s.Order, strings.ToUpper(s.Lang), s.Codec, srt),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}

	a.cancelPreview()
	a.setBusy(true, true)
	a.setStatus("Muxing subtitles into the video (no re-encode)…")

	video := a.curFile
	order := s.Order
	lang := s.Lang
	onPct, onLog := a.onPct(), a.onLog()

	go func() {
		err := ReplaceTrack(context.Background(), video, order, srt, lang, onPct, onLog)
		a.mw.Synchronize(func() {
			a.setBusy(false, false)
			if err != nil {
				a.setStatus("Error: " + err.Error())
				walk.MsgBox(a.mw, "Error", err.Error(), walk.MsgBoxIconError)
				return
			}
			a.load(video)
			a.setStatus("Done: subtitles replaced.")
		})
	}()
}

func (a *App) deleteTrack() {
	s, ok := a.curSub()
	if !ok {
		return
	}
	if walk.MsgBox(a.mw, "Confirm",
		fmt.Sprintf("Delete track #%d (%s %s) from the file?\n\nThe video file will be overwritten. Continue?",
			s.Order, strings.ToUpper(s.Lang), s.Codec),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}

	a.cancelPreview()
	a.setBusy(true, true)
	a.setStatus("Deleting the track from the video…")

	video := a.curFile
	order := s.Order
	onPct, onLog := a.onPct(), a.onLog()

	go func() {
		err := DeleteTrack(context.Background(), video, order, onPct, onLog)
		a.mw.Synchronize(func() {
			a.setBusy(false, false)
			if err != nil {
				a.setStatus("Error: " + err.Error())
				walk.MsgBox(a.mw, "Error", err.Error(), walk.MsgBoxIconError)
				return
			}
			a.load(video)
			a.setStatus("Done: track deleted.")
		})
	}()
}

func (a *App) promptNewTrack() (srtPath, title, lang string, ok bool) {
	var dlg *walk.Dialog
	var srtPathEdit, titleEdit, langEdit *walk.LineEdit

	err := Dialog{
		AssignTo: &dlg,
		Title:    "Add a new track",
		MinSize:  Size{Width: 450, Height: 150},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{},
				Children: []Widget{
					Label{Text: "SRT file:"},
					LineEdit{AssignTo: &srtPathEdit, ReadOnly: true},
					PushButton{Text: "Browse...", OnClicked: func() {
						fd := new(walk.FileDialog)
						fd.Filter = "SRT subtitles (*.srt)|*.srt"
						if ok, _ := fd.ShowOpen(dlg); ok {
							srtPathEdit.SetText(fd.FilePath)
						}
					}},
				},
			},
			Composite{
				Layout:   HBox{},
				Children: []Widget{Label{Text: "Track title:"}, LineEdit{AssignTo: &titleEdit, CueBanner: "e.g. Forced / English"}},
			},
			Composite{
				Layout:   HBox{},
				Children: []Widget{Label{Text: "Language (e.g. uk, eng):"}, LineEdit{AssignTo: &langEdit, Text: "uk"}},
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: "Add to video", OnClicked: func() {
						if strings.TrimSpace(srtPathEdit.Text()) == "" {
							walk.MsgBox(dlg, "Warning", "Select a subtitle file!", walk.MsgBoxIconWarning)
							return
						}
						dlg.Accept()
					}},
					PushButton{Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Error", "Failed to create the window", walk.MsgBoxIconError)
		return "", "", "", false
	}
	removeTitleIcon(dlg.Handle())
	if dlg.Run() != walk.DlgCmdOK {
		return "", "", "", false
	}
	return srtPathEdit.Text(), strings.TrimSpace(titleEdit.Text()), strings.TrimSpace(langEdit.Text()), true
}

func (a *App) addTrack() {
	srt, title, lang, ok := a.promptNewTrack()
	if !ok {
		return
	}

	a.cancelPreview()
	a.setBusy(true, true)
	a.setStatus("Adding the new track to the video…")

	video := a.curFile
	onPct, onLog := a.onPct(), a.onLog()
	go func() {
		err := AddTrack(context.Background(), video, srt, lang, title, onPct, onLog)
		a.mw.Synchronize(func() {
			a.setBusy(false, false)
			if err != nil {
				a.setStatus("Error: " + err.Error())
				walk.MsgBox(a.mw, "Error", err.Error(), walk.MsgBoxIconError)
				return
			}
			a.load(video)
			a.setStatus("Done: new track added.")
		})
	}()
}

func (a *App) saveAll() {
	dir := filepath.Dir(a.curFile)
	video := a.curFile
	items := append([]SubStream(nil), a.model.items...)

	outPaths := map[int]string{}
	skipped := 0
	for _, s := range items {
		if s.TextBased {
			outPaths[s.Order] = filepath.Join(dir, DefaultSrtName(video, s))
		} else {
			skipped++
		}
	}
	onPct, onLog := a.onPct(), a.onLog()
	a.setBusy(true, true)
	a.setStatus("Saving all tracks (full)…")
	go func() {
		saved, err := ExtractAllProgress(context.Background(), video, outPaths, onPct, onLog)
		a.mw.Synchronize(func() {
			a.setBusy(false, false)
			if err != nil {
				a.setStatus("Error: " + err.Error())
				walk.MsgBox(a.mw, "Error", err.Error(), walk.MsgBoxIconError)
				return
			}
			a.setStatus(fmt.Sprintf("Saved %d file(s) to %s. Skipped: %d.", saved, dir, skipped))
			walk.MsgBox(a.mw, "Done",
				fmt.Sprintf("Saved: %d\nSkipped (image): %d\nFolder: %s", saved, skipped, dir),
				walk.MsgBoxIconInformation)
		})
	}()
}
