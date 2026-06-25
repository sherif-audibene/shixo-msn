// Command shixo-cli is a terminal client for the same paste server the GUI
// talks to. Reads the GUI's config at ~/.clip/config.toml (or env vars).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sherifhamad/shixo-msn/internal/client"
	"github.com/sherifhamad/shixo-msn/internal/proto"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	api, err := newAPI()
	if err != nil && cmd != "help" && cmd != "-h" && cmd != "--help" {
		fail(err)
	}
	switch cmd {
	case "send":
		runSend(api, args)
	case "list", "ls":
		runList(api, args)
	case "get", "cat":
		runGet(api, args)
	case "rm", "del", "delete":
		runRm(api, args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `shixo-cli — terminal client for the shixo paste server

Usage:
  shixo-cli send [-t TITLE] [-d FOLDER] [TEXT]      # text from arg or stdin
  shixo-cli send -f PATH [-t TITLE] [-d FOLDER]     # file upload
  shixo-cli list [-n N] [-d FOLDER]                 # latest items
  shixo-cli get ID [-o PATH]                        # text → stdout, file → ./name or PATH
  shixo-cli rm ID                                   # delete an item

Config: reads ~/.clip/config.toml (shared with the GUI).
Override with SHIXO_URL and SHIXO_TOKEN.
`)
}

func newAPI() (*client.API, error) {
	cfg := client.Config{
		ServerURL: os.Getenv("SHIXO_URL"),
		Token:     os.Getenv("SHIXO_TOKEN"),
		Source:    os.Getenv("SHIXO_SOURCE"),
	}
	if cfg.ServerURL == "" || cfg.Token == "" {
		path, err := client.DefaultPath()
		if err != nil {
			return nil, err
		}
		loaded, err := client.Load(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (or set SHIXO_URL + SHIXO_TOKEN)", path, err)
		}
		cfg = loaded
	}
	if cfg.Source == "" {
		h, _ := os.Hostname()
		cfg.Source = h
	}
	return client.NewAPI(cfg), nil
}

func runSend(api *client.API, args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	title := fs.String("t", "", "title (optional)")
	folder := fs.String("d", "", "folder (optional)")
	file := fs.String("f", "", "file to upload (omit for text)")
	_ = fs.Parse(args)

	ctx := context.Background()

	if *file != "" {
		it, err := api.PushFile(ctx, *title, *folder, *file, progressBar(filepath.Base(*file)))
		fmt.Fprintln(os.Stderr) // newline after progress
		if err != nil {
			fail(err)
		}
		fmt.Println(it.ID)
		return
	}

	var text string
	if rest := fs.Args(); len(rest) > 0 {
		text = strings.Join(rest, " ")
	} else {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail(err)
		}
		text = string(b)
	}
	if text == "" {
		fail(fmt.Errorf("nothing to send (pass text as args or pipe via stdin, or use -f for files)"))
	}
	it, err := api.PushText(ctx, *title, *folder, text)
	if err != nil {
		fail(err)
	}
	fmt.Println(it.ID)
}

func runList(api *client.API, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	limit := fs.Int("n", 20, "max items to show (0 = all)")
	folder := fs.String("d", "", "filter by folder")
	_ = fs.Parse(args)

	items, err := api.List(context.Background())
	if err != nil {
		fail(err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN\tKIND\tSOURCE\tFOLDER\tTITLE / PREVIEW")
	shown := 0
	for _, it := range items {
		if *folder != "" && !strings.EqualFold(it.Folder, *folder) {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			it.ID,
			it.CreatedAt.Local().Format("2006-01-02 15:04"),
			it.Kind,
			it.Source,
			dash(it.Folder),
			preview(it),
		)
		shown++
		if *limit > 0 && shown >= *limit {
			break
		}
	}
	w.Flush()
}

func runGet(api *client.API, args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	out := fs.String("o", "", "output path (text: write to file instead of stdout; file: override destination)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fail(fmt.Errorf("usage: shixo-cli get ID [-o PATH]"))
	}
	id := fs.Arg(0)
	ctx := context.Background()

	// Resolve the item so we know whether it's text or file. List is the only
	// existing endpoint that gives us the metadata without downloading.
	items, err := api.List(ctx)
	if err != nil {
		fail(err)
	}
	var it *proto.Item
	for i := range items {
		if items[i].ID == id || strings.HasPrefix(items[i].ID, id) {
			it = &items[i]
			break
		}
	}
	if it == nil {
		fail(fmt.Errorf("no item with id %q", id))
	}

	if it.Kind == proto.KindText {
		txt, err := api.GetText(ctx, it.ID)
		if err != nil {
			fail(err)
		}
		if *out == "" {
			fmt.Print(txt)
			return
		}
		if err := os.WriteFile(*out, []byte(txt), 0o600); err != nil {
			fail(err)
		}
		return
	}

	dest := *out
	if dest == "" {
		dest = it.Filename
	}
	if err := api.DownloadFile(ctx, it.ID, dest, progressBar(it.Filename)); err != nil {
		fmt.Fprintln(os.Stderr)
		fail(err)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Println(dest)
}

func runRm(api *client.API, args []string) {
	if len(args) < 1 {
		fail(fmt.Errorf("usage: shixo-cli rm ID"))
	}
	if err := api.Delete(context.Background(), args[0]); err != nil {
		fail(err)
	}
}

func preview(it proto.Item) string {
	if it.Title != "" {
		return truncate(it.Title, 60)
	}
	if it.Kind == proto.KindText {
		return truncate(oneLine(it.Text), 60)
	}
	return fmt.Sprintf("%s (%s)", it.Filename, humanSize(it.Size))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ⏎ ")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanSize(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / k
	i := 0
	for f >= k && i < len(units)-1 {
		f /= k
		i++
	}
	return fmt.Sprintf("%.1f%s", f, units[i])
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// progressBar returns a ProgressFunc that prints a single self-rewriting line
// to stderr. Throttled to avoid flooding the terminal.
func progressBar(name string) client.ProgressFunc {
	var last time.Time
	return func(done, total int64) {
		if time.Since(last) < 100*time.Millisecond && done != total {
			return
		}
		last = time.Now()
		if total > 0 {
			pct := float64(done) / float64(total) * 100
			fmt.Fprintf(os.Stderr, "\r%s  %s / %s  (%5.1f%%)", name, humanSize(done), humanSize(total), pct)
		} else {
			fmt.Fprintf(os.Stderr, "\r%s  %s", name, humanSize(done))
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
