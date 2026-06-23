package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/sherifhamad/shixo-msn/internal/proto"
)

type Server struct {
	cfg     Config
	db      *DB
	store   *FileStore
	hub     *Hub
	uploads *Uploads
}

type Config struct {
	Listen      string `toml:"listen"`
	DataDir     string `toml:"data_dir"`
	Token       string `toml:"token"`
	TLSCertFile string `toml:"tls_cert"`
	TLSKeyFile  string `toml:"tls_key"`
}

func New(cfg Config, db *DB, store *FileStore, hub *Hub) *Server {
	return &Server{cfg: cfg, db: db, store: store, hub: hub, uploads: NewUploads()}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/items/text", s.auth(s.handlePushText))
	mux.HandleFunc("/api/items/file", s.auth(s.handlePushFile))
	mux.HandleFunc("/api/items", s.auth(s.handleList))
	mux.HandleFunc("/api/items/", s.auth(s.handleItem)) // /api/items/{id} and /{id}/content
	mux.HandleFunc("/api/uploads", s.auth(s.handleInitUpload))
	mux.HandleFunc("/api/uploads/", s.auth(s.handleUpload)) // chunk + finalize + abort
	mux.HandleFunc("/api/ws", s.auth(s.handleWS))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	// Web UI: serves the embedded SPA + static assets. Anything not matched
	// by the /api/* prefixes above falls through to here.
	mux.Handle("/", s.staticHandler())
	return mux
}

// auth wraps a handler with bearer-token check (token from header OR ?token= query param for WS).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := tokenFromRequest(r)
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (s *Server) handlePushText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req proto.PushTextRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	it := proto.Item{
		ID:        uuid.NewString(),
		Kind:      proto.KindText,
		Source:    req.Source,
		CreatedAt: time.Now().UTC(),
		Title:     strings.TrimSpace(req.Title),
		Folder:    strings.TrimSpace(req.Folder),
		Text:      req.Text,
		Size:      int64(len(req.Text)),
	}
	if err := s.db.Insert(it, ""); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.Publish(proto.Event{Type: proto.EventNewItem, Item: &it})
	writeJSON(w, it)
}

// handlePushFile expects a raw file body. Filename + source come from query params.
// Streaming raw is simpler and faster than multipart for arbitrary sizes.
func (s *Server) handlePushFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	filename := strings.TrimSpace(q.Get("name"))
	source := q.Get("source")
	title := strings.TrimSpace(q.Get("title"))
	folder := strings.TrimSpace(q.Get("folder"))
	if filename == "" {
		http.Error(w, "missing ?name=", http.StatusBadRequest)
		return
	}
	// strip any path components from user-supplied name
	filename = sanitizeFilename(filename)

	id := uuid.NewString()
	path, n, sha, err := s.store.Save(id, filename, r.Body)
	if err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	mime := r.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	it := proto.Item{
		ID:        id,
		Kind:      proto.KindFile,
		Source:    source,
		CreatedAt: time.Now().UTC(),
		Title:     title,
		Folder:    folder,
		Filename:  filename,
		Size:      n,
		SHA256:    sha,
		MIME:      mime,
	}
	if err := s.db.Insert(it, path); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.Publish(proto.Event{Type: proto.EventNewItem, Item: &it})
	writeJSON(w, it)
}

// handleInitUpload starts a chunked upload session. Returns the upload id +
// the recommended chunk size; the client streams chunks via PUT and then
// finalizes.
func (s *Server) handleInitUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req proto.InitUploadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Filename == "" {
		http.Error(w, "missing filename", http.StatusBadRequest)
		return
	}
	if req.Size < 0 {
		http.Error(w, "negative size", http.StatusBadRequest)
		return
	}
	filename := sanitizeFilename(req.Filename)
	id := uuid.NewString()

	// Pre-create the staging dir + the empty file so chunks can append.
	stagePath, err := s.store.UploadPath(id, filename)
	if err != nil {
		http.Error(w, "stage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	f, err := os.Create(stagePath)
	if err != nil {
		http.Error(w, "create stage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	f.Close()

	s.uploads.New(id, filename, req.Source, strings.TrimSpace(req.Title), strings.TrimSpace(req.Folder), req.Size)
	writeJSON(w, proto.InitUploadResponse{UploadID: id, ChunkSize: RecommendedChunkSize})
}

// handleUpload dispatches PUT /api/uploads/{id}/chunk?offset=N,
// POST /api/uploads/{id}/finalize, and DELETE /api/uploads/{id}.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/uploads/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	sess, ok := s.uploads.Get(id)
	if !ok {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodDelete && len(parts) == 1 {
		s.uploads.Drop(id)
		_ = s.store.AbortUpload(id)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	switch parts[1] {
	case "chunk":
		if r.Method != http.MethodPut {
			http.Error(w, "PUT only", http.StatusMethodNotAllowed)
			return
		}
		offsetStr := r.URL.Query().Get("offset")
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil || offset < 0 {
			http.Error(w, "bad offset", http.StatusBadRequest)
			return
		}
		stagePath, err := s.store.UploadPath(id, sess.Filename)
		if err != nil {
			http.Error(w, "stage: "+err.Error(), http.StatusInternalServerError)
			return
		}
		f, err := os.OpenFile(stagePath, os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, "open: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if _, err := f.Seek(offset, 0); err != nil {
			http.Error(w, "seek: "+err.Error(), http.StatusInternalServerError)
			return
		}
		n, err := io.Copy(f, r.Body)
		if err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Bump the session's "received" counter; this is monotonic-ish since
		// retries with the same offset just overwrite the same bytes.
		_ = s.uploads.Touch(id, n)
		writeJSON(w, map[string]any{"received": n, "offset": offset + n})

	case "finalize":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		itemID := uuid.NewString()
		path, size, sha, err := s.store.FinalizeUpload(id, itemID, sess.Filename)
		if err != nil {
			http.Error(w, "finalize: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.uploads.Drop(id)

		// Best-effort size check (declared size may be 0 if client didn't know).
		if sess.Size > 0 && sess.Size != size {
			// Don't fail — the file is what it is. Just log via a warning header.
			w.Header().Set("X-Size-Mismatch", strconv.FormatInt(sess.Size-size, 10))
		}

		it := proto.Item{
			ID:        itemID,
			Kind:      proto.KindFile,
			Source:    sess.Source,
			CreatedAt: time.Now().UTC(),
			Title:     sess.Title,
			Folder:    sess.Folder,
			Filename:  sess.Filename,
			Size:      size,
			SHA256:    sha,
			MIME:      "application/octet-stream",
		}
		if err := s.db.Insert(it, path); err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.hub.Publish(proto.Event{Type: proto.EventNewItem, Item: &it})
		writeJSON(w, it)

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.db.List(200)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, proto.ListResponse{Items: items})
}

// handleItem dispatches /api/items/{id} and /api/items/{id}/content.
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/items/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	it, path, err := s.db.Get(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete && len(parts) == 1 {
		// Remove files first; if that fails the row stays so a retry can clean up.
		if it.Kind == proto.KindFile {
			if err := s.store.Delete(id); err != nil {
				http.Error(w, "rm files: "+err.Error(), http.StatusInternalServerError)
				return
			}
			_ = path // path captured for symmetry; the store deletes by id
		}
		if err := s.db.Delete(id); err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.hub.Publish(proto.Event{Type: proto.EventDeleted, ID: id})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPatch && len(parts) == 1 {
		var upd proto.UpdateItemRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024*1024)).Decode(&upd); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Trim whitespace on string fields so " " doesn't sneak in as a value.
		if upd.Title != nil {
			t := strings.TrimSpace(*upd.Title)
			upd.Title = &t
		}
		if upd.Folder != nil {
			f := strings.TrimSpace(*upd.Folder)
			upd.Folder = &f
		}
		if err := s.db.Update(id, upd); err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
			return
		}
		newItem, _, err := s.db.Get(id)
		if err != nil {
			http.Error(w, "reread: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.hub.Publish(proto.Event{Type: proto.EventUpdated, Item: &newItem})
		writeJSON(w, newItem)
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		s.serveContent(w, r, it, path)
		return
	}
	writeJSON(w, it)
}

func (s *Server) serveContent(w http.ResponseWriter, r *http.Request, it proto.Item, path string) {
	if it.Kind == proto.KindText {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, it.Text)
		return
	}
	f, err := s.store.Open(path)
	if err != nil {
		http.Error(w, "open: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if it.MIME != "" {
		w.Header().Set("Content-Type", it.MIME)
	}
	// quoted to be safe with unicode/spaces
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(it.Filename))
	http.ServeContent(w, r, it.Filename, it.CreatedAt, f)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin check: token-based, public clients
	})
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}
	defer c.CloseNow()

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// reader: drop incoming frames + detect disconnect
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	// ping loop
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsjson.Write(wctx, c, ev)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
		log.Printf("write json: %v", err)
	}
}

func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" || s == "." || s == ".." {
		return "file"
	}
	return s
}
