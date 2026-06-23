// Command shixo-msn is the desktop window app: paste text or drop files in,
// they sync to all other machines connected to the same server.
package main

import (
	"context"
	"flag"
	"fmt"
	"image/color"
	"log"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/gen2brain/beeep"
	"github.com/sherifhamad/shixo-msn/internal/client"
	"github.com/sherifhamad/shixo-msn/internal/proto"
)

const appID = "com.sherifhamad.shixo-msn"

var (
	colorOK    = color.NRGBA{R: 76, G: 175, B: 80, A: 255}  // green
	colorWarn  = color.NRGBA{R: 251, G: 188, B: 5, A: 255}  // amber
	colorError = color.NRGBA{R: 234, G: 67, B: 53, A: 255}  // red
)

type ui struct {
	app    fyne.App
	win    fyne.Window
	cfg    client.Config
	api    *client.API
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	items    []proto.Item // full history, newest first
	filtered []proto.Item // items after the search query filter
	query    string       // current search text (lowercased)
	folder   string       // current folder filter; "" == all

	list       *widget.List
	titleBox   *widget.Entry
	pasteBox   *widget.Entry
	folderBox  *widget.SelectEntry
	folderFilt *widget.Select
	searchBox  *widget.Entry
	statusDot  *canvas.Text
	statusText *widget.Label
	progress   *widget.ProgressBar
	progLbl    *widget.Label
	refreshBtn *widget.Button

	subMu     sync.Mutex
	subCancel context.CancelFunc // cancels the current subscribe goroutine

	rowsMu sync.Mutex
	rows   map[fyne.CanvasObject]*historyRow
}

const passwordFolder = "passwords"
const passwordMask = "••••••••"

// isPasswordItem returns true for text items in the "passwords" folder
// (case-insensitive). File items are never masked.
func isPasswordItem(it proto.Item) bool {
	return it.Kind == proto.KindText && strings.EqualFold(it.Folder, passwordFolder)
}

// historyRow bundles every widget in one list row so updateRow doesn't have
// to navigate the container tree (Fyne's NewBorder ordering shifted between
// 2.5 and 2.7 — the panic at main.go:242 came from that).
type historyRow struct {
	box       *fyne.Container
	icon      *widget.Icon
	timeLbl   *widget.Label
	srcLbl    *widget.Label
	folderLbl *widget.Label // small italic, e.g. "code"
	titleLbl  *widget.Label // bold; only shown when item.Title != ""
	preview   *widget.Label
	primary   *widget.Button
	del       *widget.Button
}

const folderAll = "All folders"
const folderNone = "Uncategorized"

func main() {
	hidden := flag.Bool("hidden", false, "start with the window hidden (used by autostart)")
	flag.Parse()

	a := fyneapp.NewWithID(appID)
	w := a.NewWindow("shixo-msn")
	w.Resize(fyne.NewSize(820, 600))
	// Closing the window hides it instead of quitting the app — so the tray
	// menu and on-arrival pop-up can bring it back without restarting.
	w.SetCloseIntercept(func() { w.Hide() })

	u := &ui{app: a, win: w, rows: map[fyne.CanvasObject]*historyRow{}}
	u.ctx, u.cancel = context.WithCancel(context.Background())

	// System tray (mac menu bar / Windows tray / Linux indicator).
	if desk, ok := a.(desktop.App); ok {
		menu := fyne.NewMenu("shixo-msn",
			fyne.NewMenuItem("Show", func() {
				fyne.Do(func() {
					w.Show()
					w.RequestFocus()
				})
			}),
			fyne.NewMenuItem("Quit", func() { a.Quit() }),
		)
		desk.SetSystemTrayMenu(menu)
	}

	cfgPath, err := client.DefaultPath()
	if err != nil {
		dialog.ShowError(err, w)
		w.ShowAndRun()
		return
	}

	cfg, err := client.Load(cfgPath)
	if err != nil {
		u.runSetup(cfgPath, func(c client.Config) {
			u.cfg = c
			u.api = client.NewAPI(c)
			u.buildMain()
			u.start()
		})
	} else {
		u.cfg = cfg
		u.api = client.NewAPI(cfg)
		u.buildMain()
		u.start()
	}

	if *hidden {
		// Window is built but never shown — autostart path. The tray menu and
		// on-arrival pop-up can show it; close-intercept hides it again.
		a.Run()
	} else {
		w.ShowAndRun()
	}
}

// runSetup shows a tiny first-run form: server URL + token.
func (u *ui) runSetup(savePath string, done func(client.Config)) {
	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://paste.example.com")
	tokenEntry := widget.NewPasswordEntry()
	tokenEntry.SetPlaceHolder("shared bearer token")

	form := widget.NewForm(
		widget.NewFormItem("Server URL", urlEntry),
		widget.NewFormItem("Token", tokenEntry),
	)
	form.SubmitText = "Save"
	form.OnSubmit = func() {
		c := client.Config{ServerURL: urlEntry.Text, Token: tokenEntry.Text}
		if err := client.Save(savePath, c); err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		c, err := client.Load(savePath)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		done(c)
	}
	u.win.SetContent(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("First-time setup", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Saved to "+savePath),
		form,
	)))
}

// buildMain assembles the main window.
func (u *ui) buildMain() {
	u.statusDot = canvas.NewText("●", colorWarn)
	u.statusDot.TextSize = 16
	u.statusText = widget.NewLabel("connecting…")
	u.refreshBtn = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() { u.reconnect() })

	header := container.NewHBox(
		u.statusDot,
		u.statusText,
		u.refreshBtn,
		layout.NewSpacer(),
		widget.NewLabelWithStyle(shortServer(u.cfg.ServerURL), fyne.TextAlignTrailing, fyne.TextStyle{Italic: true}),
	)

	u.titleBox = widget.NewEntry()
	u.titleBox.SetPlaceHolder("Title (optional)")

	u.folderBox = widget.NewSelectEntry(nil) // options populated from items later
	u.folderBox.SetPlaceHolder("Folder")

	u.pasteBox = widget.NewMultiLineEntry()
	u.pasteBox.SetPlaceHolder("Paste text here, then click Send Text — or drop a file anywhere.")
	u.pasteBox.Wrapping = fyne.TextWrapWord
	// Cap min height so the paste area doesn't dominate when empty.
	pasteScroll := container.NewVScroll(u.pasteBox)
	pasteScroll.SetMinSize(fyne.NewSize(0, 100))

	sendText := widget.NewButtonWithIcon("Send Text", theme.MailSendIcon(), func() {
		txt := u.pasteBox.Text
		if txt == "" {
			return
		}
		title := strings.TrimSpace(u.titleBox.Text)
		folder := strings.TrimSpace(u.folderBox.Text)
		u.pasteBox.SetText("")
		u.titleBox.SetText("")
		u.folderBox.SetText("")
		go u.sendText(title, folder, txt)
	})
	sendText.Importance = widget.HighImportance

	sendFile := widget.NewButtonWithIcon("Send File…", theme.UploadIcon(), func() {
		dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				return
			}
			path := rc.URI().Path()
			rc.Close()
			title := strings.TrimSpace(u.titleBox.Text)
			folder := strings.TrimSpace(u.folderBox.Text)
			u.titleBox.SetText("")
			u.folderBox.SetText("")
			go u.sendFile(title, folder, path)
		}, u.win)
	})

	u.progress = widget.NewProgressBar()
	u.progress.Hide()
	u.progLbl = widget.NewLabel("")
	u.progLbl.Hide()

	composeButtons := container.NewHBox(sendText, sendFile, layout.NewSpacer(), u.progLbl)
	// Title takes 2/3 width, folder takes 1/3 — adapter splits using a Grid.
	titleFolderRow := container.New(layout.NewGridLayoutWithColumns(2), u.titleBox, u.folderBox)
	compose := container.NewBorder(
		titleFolderRow,                                  // top
		container.NewVBox(composeButtons, u.progress),    // bottom
		nil, nil,
		pasteScroll,
	)

	historyHeader := widget.NewLabelWithStyle("History", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	u.searchBox = widget.NewEntry()
	u.searchBox.SetPlaceHolder("Search history…")
	u.searchBox.OnChanged = func(s string) {
		u.query = strings.ToLower(strings.TrimSpace(s))
		u.applyFilter()
	}

	u.folderFilt = widget.NewSelect([]string{folderAll}, func(v string) {
		switch v {
		case folderAll, "":
			u.folder = ""
		case folderNone:
			u.folder = "\x00" // sentinel: match only items with empty Folder
		default:
			u.folder = v
		}
		// Belt-and-braces: applyFilter calls fyne.Do(), which deadlocks if
		// the main event loop isn't running yet (e.g. if something fires
		// OnChanged during buildMain before ShowAndRun starts the loop).
		if u.list != nil {
			u.applyFilter()
		}
	})
	// Pre-seed the visible selection without firing OnChanged. Going through
	// SetSelected here would call applyFilter → fyne.Do, which blocks waiting
	// for the main loop that ShowAndRun hasn't started yet — the symptom is
	// the process hanging silently with no window. Setting the field directly
	// is safe; the first refresh() after start() will paint it correctly.
	u.folderFilt.Selected = folderAll

	searchBar := container.NewBorder(nil, nil, historyHeader, u.folderFilt, u.searchBox)

	u.list = widget.NewList(
		func() int { u.mu.Lock(); defer u.mu.Unlock(); return len(u.filtered) },
		u.newRow,
		u.updateRow,
	)
	u.list.OnSelected = func(i widget.ListItemID) {
		u.mu.Lock()
		if i < 0 || i >= len(u.filtered) {
			u.mu.Unlock()
			return
		}
		it := u.filtered[i]
		u.mu.Unlock()
		u.list.UnselectAll()
		u.showDetail(it)
	}

	top := container.NewVBox(header, widget.NewSeparator(), compose, widget.NewSeparator(), searchBar)
	u.win.SetContent(container.NewBorder(top, nil, nil, nil, u.list))

	// Drag a file from Finder / Explorer / Files anywhere on the window.
	// If the title box has a value when the drop lands, use it for the first
	// dropped file only (a per-batch title makes more sense than multi-file).
	u.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		title := strings.TrimSpace(u.titleBox.Text)
		folder := strings.TrimSpace(u.folderBox.Text)
		u.titleBox.SetText("")
		u.folderBox.SetText("")
		first := true
		for _, uri := range uris {
			if uri == nil {
				continue
			}
			path := uri.Path()
			if path == "" {
				continue
			}
			t := ""
			if first {
				t = title
				first = false
			}
			go u.sendFile(t, folder, path)
		}
	})

	u.win.SetOnClosed(func() { u.cancel() })
}

// newRow builds a history row and indexes its widget refs by container
// pointer so updateRow doesn't depend on Fyne's positional Objects layout.
func (u *ui) newRow() fyne.CanvasObject {
	r := &historyRow{
		icon:      widget.NewIcon(theme.DocumentIcon()),
		timeLbl:   widget.NewLabel(""),
		srcLbl:    widget.NewLabel(""),
		folderLbl: widget.NewLabel(""),
		titleLbl:  widget.NewLabel(""),
		preview:   widget.NewLabel(""),
		primary:   widget.NewButton("Copy", nil),
		del:       widget.NewButtonWithIcon("", theme.DeleteIcon(), nil),
	}
	r.srcLbl.TextStyle = fyne.TextStyle{Italic: true}
	r.folderLbl.TextStyle = fyne.TextStyle{Italic: true}
	r.titleLbl.TextStyle = fyne.TextStyle{Bold: true}
	r.titleLbl.Truncation = fyne.TextTruncateEllipsis
	r.preview.Truncation = fyne.TextTruncateEllipsis
	r.primary.Importance = widget.LowImportance
	r.del.Importance = widget.DangerImportance

	left := container.NewHBox(r.icon, r.timeLbl, r.srcLbl)
	right := container.NewHBox(r.folderLbl, r.primary, r.del)
	// Center column: title on top (collapsed when empty), preview below.
	center := container.NewVBox(r.titleLbl, r.preview)
	r.box = container.NewBorder(nil, nil, left, right, center)

	u.rowsMu.Lock()
	u.rows[r.box] = r
	u.rowsMu.Unlock()
	return r.box
}

func (u *ui) updateRow(i widget.ListItemID, o fyne.CanvasObject) {
	u.mu.Lock()
	if i < 0 || i >= len(u.filtered) {
		u.mu.Unlock()
		return
	}
	it := u.filtered[i]
	u.mu.Unlock()

	u.rowsMu.Lock()
	r := u.rows[o]
	u.rowsMu.Unlock()
	if r == nil {
		return
	}

	r.timeLbl.SetText(it.CreatedAt.Local().Format("15:04"))
	r.srcLbl.SetText(shortSource(it.Source))

	if it.Folder != "" {
		r.folderLbl.SetText("[ " + it.Folder + " ]")
		r.folderLbl.Show()
	} else {
		r.folderLbl.SetText("")
		r.folderLbl.Hide()
	}

	if it.Title != "" {
		r.titleLbl.SetText(it.Title)
		r.titleLbl.Show()
	} else {
		r.titleLbl.SetText("")
		r.titleLbl.Hide()
	}

	if it.Kind == proto.KindText {
		r.icon.SetResource(theme.DocumentIcon())
		if isPasswordItem(it) {
			r.preview.SetText(passwordMask)
		} else {
			r.preview.SetText(oneLine(it.Text))
		}
		r.primary.SetText("Copy")
		r.primary.SetIcon(theme.ContentCopyIcon())
		r.primary.OnTapped = func() { u.copyText(it) }
	} else {
		r.icon.SetResource(theme.FileIcon())
		r.preview.SetText(it.Filename + "  ·  " + formatSize(it.Size))
		r.primary.SetText("Save")
		r.primary.SetIcon(theme.DownloadIcon())
		r.primary.OnTapped = func() { u.saveFile(it) }
	}

	r.del.OnTapped = func() { u.confirmDelete(it) }
}

// applyFilter recomputes the visible list from u.items + u.query + u.folder.
func (u *ui) applyFilter() {
	u.mu.Lock()
	folders := uniqueFolders(u.items)
	q := u.query
	f := u.folder
	out := make([]proto.Item, 0, len(u.items))
	for _, it := range u.items {
		if !folderMatches(it, f) {
			continue
		}
		if !itemMatches(it, q) {
			continue
		}
		out = append(out, it)
	}
	u.filtered = out
	u.mu.Unlock()
	fyne.Do(func() {
		u.list.Refresh()
		u.refreshFolderOptions(folders)
	})
}

// refreshFolderOptions updates the folder filter dropdown + the compose
// SelectEntry's suggestion list with the current set of folders. Keeps the
// user's current selections.
func (u *ui) refreshFolderOptions(folders []string) {
	// Filter dropdown: All, Uncategorized, then folders alphabetically.
	opts := []string{folderAll, folderNone}
	opts = append(opts, folders...)
	cur := u.folderFilt.Selected
	u.folderFilt.SetOptions(opts)
	if cur != "" {
		u.folderFilt.SetSelected(cur) // restore (no-op if option vanished)
	}
	// Compose SelectEntry: only real folders as suggestions.
	u.folderBox.SetOptions(folders)
}

// uniqueFolders returns sorted distinct non-empty folder names.
func uniqueFolders(items []proto.Item) []string {
	seen := map[string]struct{}{}
	for _, it := range items {
		if it.Folder != "" {
			seen[it.Folder] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// folderMatches: "" = any, "\x00" = only items with empty Folder, else exact.
func folderMatches(it proto.Item, filter string) bool {
	switch filter {
	case "":
		return true
	case "\x00":
		return it.Folder == ""
	default:
		return it.Folder == filter
	}
}

// itemMatches: case-insensitive substring across title, folder, text, filename, source.
func itemMatches(it proto.Item, q string) bool {
	if q == "" {
		return true
	}
	hay := strings.ToLower(it.Title + "\n" + it.Folder + "\n" + it.Filename + "\n" + it.Source + "\n" + it.Text)
	return strings.Contains(hay, q)
}

// showDetail opens a modal with the full content + actions. Has two modes:
// read-only (default) and edit (toggled via the Edit button). Edit lets you
// change title + folder for any item, and the text for text items.
func (u *ui) showDetail(it proto.Item) {
	header := it.CreatedAt.Local().Format("Mon 15:04:05") + "   from " + it.Source
	if it.Title != "" {
		header = it.Title + "   ·   " + header
	}

	editing := false
	isPwd := isPasswordItem(it)
	revealPwd := false // toggled by Show/Hide; auto-true in edit mode

	// Editable inputs (created once, populated below; visibility flips with mode).
	titleIn := widget.NewEntry()
	titleIn.SetText(it.Title)
	titleIn.SetPlaceHolder("Title (optional)")

	u.mu.Lock()
	folderOpts := uniqueFolders(u.items)
	u.mu.Unlock()
	folderIn := widget.NewSelectEntry(folderOpts)
	folderIn.SetText(it.Folder)
	folderIn.SetPlaceHolder("Folder")

	// View body (read-only) vs edit body
	textEntry := widget.NewMultiLineEntry()
	textEntry.SetText(it.Text)
	textEntry.Wrapping = fyne.TextWrapWord
	textScroll := container.NewVScroll(textEntry)
	textScroll.SetMinSize(fyne.NewSize(560, 260))

	showBtn := widget.NewButtonWithIcon("Show", theme.VisibilityIcon(), nil)
	showBtn.Hide()
	// renderText paints textEntry according to the current mode + reveal state.
	// In edit mode, always show real text (the user is editing it). In view
	// mode, mask the text unless the user clicked Show.
	renderText := func() {
		if !isPwd || it.Kind != proto.KindText {
			return
		}
		if editing || revealPwd {
			textEntry.SetText(it.Text)
		} else {
			textEntry.SetText(passwordMask)
		}
	}
	if isPwd && it.Kind == proto.KindText {
		showBtn.Show()
		showBtn.OnTapped = func() {
			revealPwd = !revealPwd
			if revealPwd {
				showBtn.SetText("Hide")
				showBtn.SetIcon(theme.VisibilityOffIcon())
			} else {
				showBtn.SetText("Show")
				showBtn.SetIcon(theme.VisibilityIcon())
			}
			renderText()
		}
		renderText()
	}

	fileInfo := widget.NewForm(
		widget.NewFormItem("Filename", widget.NewLabel(it.Filename)),
		widget.NewFormItem("Size", widget.NewLabel(formatSize(it.Size))),
		widget.NewFormItem("SHA-256", widget.NewLabel(it.SHA256)),
		widget.NewFormItem("Source", widget.NewLabel(it.Source)),
		widget.NewFormItem("When", widget.NewLabel(it.CreatedAt.Local().Format(time.RFC1123))),
	)

	// metaForm holds title + folder; visible only in edit mode.
	metaForm := container.New(layout.NewGridLayoutWithColumns(2), titleIn, folderIn)
	metaForm.Hide()

	var body fyne.CanvasObject
	if it.Kind == proto.KindText {
		body = container.NewBorder(metaForm, nil, nil, nil, textScroll)
	} else {
		body = container.NewBorder(metaForm, nil, nil, nil, fileInfo)
		// Files: text edits not allowed; disable the entry as a precaution.
		textEntry.Disable()
	}

	var d dialog.Dialog
	closeBtn := widget.NewButton("Close", func() { d.Hide() })

	primary := widget.NewButton("", nil) // text label varies
	primary.Importance = widget.HighImportance
	delBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		d.Hide()
		u.confirmDelete(it)
	})
	delBtn.Importance = widget.DangerImportance

	editBtn := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), nil)
	cancelBtn := widget.NewButton("Cancel", nil)
	cancelBtn.Hide()

	// applyMode wires the visible widgets + primary button label/action to
	// the current mode. Centralizing this avoids state drift.
	applyMode := func() {
		if editing {
			metaForm.Show()
			editBtn.Hide()
			cancelBtn.Show()
			showBtn.Hide() // edit mode reveals text by definition
			renderText()
			primary.SetText("Save")
			primary.SetIcon(theme.ConfirmIcon())
			primary.OnTapped = func() {
				newTitle := strings.TrimSpace(titleIn.Text)
				newFolder := strings.TrimSpace(folderIn.Text)
				newText := textEntry.Text
				go func() {
					upd := proto.UpdateItemRequest{Title: &newTitle, Folder: &newFolder}
					if it.Kind == proto.KindText {
						upd.Text = &newText
					}
					if _, err := u.api.Update(u.ctx, it.ID, upd); err != nil {
						u.showErr(err)
						return
					}
					fyne.Do(func() { d.Hide() })
				}()
			}
		} else {
			metaForm.Hide()
			editBtn.Show()
			cancelBtn.Hide()
			if isPwd && it.Kind == proto.KindText {
				showBtn.Show()
				renderText()
			} else {
				showBtn.Hide()
			}
			if it.Kind == proto.KindText {
				primary.SetText("Copy")
				primary.SetIcon(theme.ContentCopyIcon())
				primary.OnTapped = func() {
					u.copyText(it)
					d.Hide()
				}
			} else {
				primary.SetText("Save…")
				primary.SetIcon(theme.DownloadIcon())
				primary.OnTapped = func() {
					u.saveFile(it)
					d.Hide()
				}
			}
		}
	}

	editBtn.OnTapped = func() { editing = true; applyMode() }
	cancelBtn.OnTapped = func() {
		// reset inputs to original
		titleIn.SetText(it.Title)
		folderIn.SetText(it.Folder)
		if it.Kind == proto.KindText {
			textEntry.SetText(it.Text)
		}
		editing = false
		applyMode() // renderText restores the mask if this is a password item
	}
	applyMode()

	actions := container.NewBorder(
		nil, nil,
		delBtn,
		container.NewHBox(showBtn, editBtn, cancelBtn, closeBtn, primary),
		nil,
	)
	content := container.NewBorder(nil, container.NewVBox(widget.NewSeparator(), actions), nil, nil, body)

	d = dialog.NewCustomWithoutButtons(header, content, u.win)
	d.Resize(fyne.NewSize(680, 500))
	d.Show()
}

// confirmDelete shows a small confirmation, then deletes on confirm.
func (u *ui) confirmDelete(it proto.Item) {
	msg := "Remove this item from all machines?"
	if it.Kind == proto.KindFile {
		msg = "Remove " + it.Filename + " (" + formatSize(it.Size) + ") from all machines?"
	}
	dialog.ShowConfirm("Delete?", msg, func(ok bool) {
		if !ok {
			return
		}
		go func() {
			if err := u.api.Delete(u.ctx, it.ID); err != nil {
				u.showErr(err)
			}
			// list refreshes via the EventDeleted websocket frame
		}()
	}, u.win)
}

// start kicks off the initial list fetch and the websocket subscription.
func (u *ui) start() {
	go u.refresh()
	go u.startSubscribe()
}

// startSubscribe (re)starts the websocket goroutine. If one is already
// running, its context is canceled first so the old loop exits before the
// new one connects.
func (u *ui) startSubscribe() {
	u.subMu.Lock()
	if u.subCancel != nil {
		u.subCancel()
	}
	ctx, cancel := context.WithCancel(u.ctx)
	u.subCancel = cancel
	u.subMu.Unlock()
	u.subscribeLoop(ctx)
}

// reconnect is wired to the header refresh button: re-fetch the history and
// force the websocket to drop and reconnect immediately, skipping any
// backoff the current subscribe goroutine is sitting in.
func (u *ui) reconnect() {
	go u.refresh()
	go u.startSubscribe()
}

func (u *ui) refresh() {
	items, err := u.api.List(u.ctx)
	if err != nil {
		u.setStatus(colorError, "offline: "+err.Error())
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	u.mu.Lock()
	u.items = items
	u.mu.Unlock()
	u.applyFilter()
}

func (u *ui) subscribeLoop(ctx context.Context) {
	u.setStatus(colorWarn, "connecting…")
	ch := client.Subscribe(ctx, u.cfg)
	u.setStatus(colorOK, "connected")
	for ev := range ch {
		switch ev.Type {
		case proto.EventNewItem:
			if ev.Item == nil {
				continue
			}
			it := *ev.Item
			u.mu.Lock()
			u.items = append([]proto.Item{it}, u.items...)
			u.mu.Unlock()
			u.applyFilter()
			if it.Source != u.cfg.Source {
				u.notifyArrival(it)
			}
		case proto.EventUpdated:
			if ev.Item == nil {
				continue
			}
			upd := *ev.Item
			u.mu.Lock()
			for i, x := range u.items {
				if x.ID == upd.ID {
					u.items[i] = upd
					break
				}
			}
			u.mu.Unlock()
			u.applyFilter()
		case proto.EventDeleted:
			id := ev.ID
			u.mu.Lock()
			out := u.items[:0]
			for _, x := range u.items {
				if x.ID != id {
					out = append(out, x)
				}
			}
			u.items = out
			u.mu.Unlock()
			u.applyFilter()
		}
	}
	u.setStatus(colorError, "disconnected")
}

func (u *ui) notifyArrival(it proto.Item) {
	title := "shixo-msn"
	var body string
	if it.Kind == proto.KindText {
		body = it.Source + ": " + truncate(oneLine(it.Text), 80)
	} else {
		body = it.Source + ": file " + it.Filename + " (" + formatSize(it.Size) + ")"
	}
	_ = beeep.Notify(title, body, "")
	go func() {
		_ = beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	}()
	fyne.Do(func() {
		u.win.Show()
		u.win.RequestFocus()
	})
}

func (u *ui) sendText(title, folder, txt string) {
	u.setBusy("sending text…", -1)
	defer u.clearBusy()
	if _, err := u.api.PushText(u.ctx, title, folder, txt); err != nil {
		u.showErr(err)
	}
}

func (u *ui) sendFile(title, folder, path string) {
	name := filepath.Base(path)
	u.setBusy("uploading "+name+"…", 0)
	defer u.clearBusy()
	_, err := u.api.PushFile(u.ctx, title, folder, path, func(done, total int64) {
		u.setProgress(fmt.Sprintf("uploading %s — %s / %s", name, formatSize(done), formatSize(total)), done, total)
	})
	if err != nil {
		u.showErr(err)
	}
}

func (u *ui) copyText(it proto.Item) {
	go func() {
		text := it.Text
		if text == "" {
			t, err := u.api.GetText(u.ctx, it.ID)
			if err != nil {
				u.showErr(err)
				return
			}
			text = t
		}
		fyne.Do(func() {
			u.win.Clipboard().SetContent(text)
		})
	}()
}

func (u *ui) saveFile(it proto.Item) {
	fs := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil || wc == nil {
			return
		}
		destURI := wc.URI()
		wc.Close() // re-opened with os.Create below for streaming
		dest := destURI.Path()
		go func() {
			u.setBusy("downloading "+it.Filename+"…", 0)
			defer u.clearBusy()
			err := u.api.DownloadFile(u.ctx, it.ID, dest, func(done, total int64) {
				u.setProgress(fmt.Sprintf("downloading %s — %s / %s", it.Filename, formatSize(done), formatSize(total)), done, total)
			})
			if err != nil {
				u.showErr(err)
				return
			}
			fyne.Do(func() {
				dialog.ShowInformation("Saved", "Saved to "+dest, u.win)
			})
		}()
	}, u.win)
	// Pre-fill the original filename so the user doesn't have to retype it.
	fs.SetFileName(it.Filename)
	fs.Show()
}

func (u *ui) setStatus(c color.Color, s string) {
	fyne.Do(func() {
		u.statusDot.Color = c
		u.statusDot.Refresh()
		u.statusText.SetText(s)
	})
}

func (u *ui) setBusy(msg string, totalHint int64) {
	fyne.Do(func() {
		u.progLbl.SetText(msg)
		u.progLbl.Show()
		if totalHint > 0 {
			u.progress.Min, u.progress.Max = 0, float64(totalHint)
			u.progress.SetValue(0)
		} else {
			u.progress.Min, u.progress.Max = 0, 1
			u.progress.SetValue(0)
		}
		u.progress.Show()
	})
}

func (u *ui) setProgress(msg string, done, total int64) {
	fyne.Do(func() {
		u.progLbl.SetText(msg)
		if total > 0 {
			u.progress.Max = float64(total)
			u.progress.SetValue(float64(done))
		}
	})
}

func (u *ui) clearBusy() {
	time.Sleep(150 * time.Millisecond) // brief lingering so the bar doesn't blink off mid-frame
	fyne.Do(func() {
		u.progress.Hide()
		u.progLbl.Hide()
		u.progLbl.SetText("")
	})
}

func (u *ui) showErr(err error) {
	log.Printf("err: %v", err)
	fyne.Do(func() { dialog.ShowError(err, u.win) })
}

// shortSource collapses a host like "Sherifs-MacBook-Pro.local" to "Sherifs"
// or "openssh-debian-server" to "openssh". Keeps fully short names intact.
func shortSource(s string) string {
	for _, suf := range []string{".local", ".lan", ".home", ".internal"} {
		s = strings.TrimSuffix(s, suf)
	}
	if i := strings.Index(s, "-"); i > 0 && i <= 12 {
		return s[:i]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// shortServer collapses "https://paste.sherifs.de/" to "paste.sherifs.de".
func shortServer(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	return u.Host
}

// oneLine collapses CR/LF in a preview so adjacent rows don't overlap.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ↵ ")
	s = strings.ReplaceAll(s, "\n", " ↵ ")
	s = strings.ReplaceAll(s, "\r", " ↵ ")
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func formatSize(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / k
	i := 0
	for f >= k && i < len(units)-1 {
		f /= k
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
