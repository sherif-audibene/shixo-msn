package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sherifhamad/shixo-msn/internal/proto"
)

// API is a thin HTTP client for the server.
type API struct {
	cfg  Config
	http *http.Client
}

func NewAPI(cfg Config) *API {
	return &API{
		cfg: cfg,
		http: &http.Client{
			// No global timeout: large transfers must be allowed to take as long as they need.
			Timeout: 0,
		},
	}
}

// ProgressFunc is called with bytes-so-far and total (or -1 if unknown).
type ProgressFunc func(done, total int64)

func (a *API) authReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := strings.TrimRight(a.cfg.ServerURL, "/") + path
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	return r, nil
}

func (a *API) PushText(ctx context.Context, title, folder, text string) (proto.Item, error) {
	body, _ := json.Marshal(proto.PushTextRequest{Title: title, Folder: folder, Text: text, Source: a.cfg.Source})
	r, err := a.authReq(ctx, http.MethodPost, "/api/items/text", bytes.NewReader(body))
	if err != nil {
		return proto.Item{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(r)
	if err != nil {
		return proto.Item{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return proto.Item{}, fmt.Errorf("push text: %s: %s", resp.Status, b)
	}
	var it proto.Item
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
		return proto.Item{}, err
	}
	return it, nil
}

// ChunkUploadThreshold is the file size at which the client switches from the
// single-shot endpoint to chunked uploads. Anything above this would risk
// hitting Cloudflare's ~100MB request body cap, so we chunk well below it.
const ChunkUploadThreshold int64 = 40 * 1024 * 1024 // 40MB

// DefaultChunkSize is the chunk size the client uses if init didn't return one.
const DefaultChunkSize int64 = 32 * 1024 * 1024 // 32MB

// PushFile uploads a file. Small files go straight through; larger files are
// uploaded in chunks via the /api/uploads/* endpoints so we don't trip
// Cloudflare's body-size limits.
func (a *API) PushFile(ctx context.Context, title, folder, path string, progress ProgressFunc) (proto.Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return proto.Item{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return proto.Item{}, err
	}
	total := stat.Size()
	name := filepath.Base(path)

	if total > ChunkUploadThreshold {
		return a.pushFileChunked(ctx, f, name, title, folder, total, progress)
	}

	pr := &progressReader{r: f, total: total, fn: progress}

	q := url.Values{}
	q.Set("name", name)
	q.Set("source", a.cfg.Source)
	if title != "" {
		q.Set("title", title)
	}
	if folder != "" {
		q.Set("folder", folder)
	}
	r, err := a.authReq(ctx, http.MethodPost, "/api/items/file?"+q.Encode(), pr)
	if err != nil {
		return proto.Item{}, err
	}
	r.ContentLength = total
	r.Header.Set("Content-Type", "application/octet-stream")
	resp, err := a.http.Do(r)
	if err != nil {
		return proto.Item{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return proto.Item{}, fmt.Errorf("push file: %s: %s", resp.Status, b)
	}
	var it proto.Item
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
		return proto.Item{}, err
	}
	return it, nil
}

// pushFileChunked streams the file in fixed-size chunks. Each chunk is a
// separate PUT with the body's content-length set, so Cloudflare sees small
// requests and never trips its body-size limit.
func (a *API) pushFileChunked(ctx context.Context, f *os.File, name, title, folder string, total int64, progress ProgressFunc) (proto.Item, error) {
	// 1. init
	initBody, _ := json.Marshal(proto.InitUploadRequest{
		Filename: name, Size: total, Source: a.cfg.Source, Title: title, Folder: folder,
	})
	r, err := a.authReq(ctx, http.MethodPost, "/api/uploads", bytes.NewReader(initBody))
	if err != nil {
		return proto.Item{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(r)
	if err != nil {
		return proto.Item{}, err
	}
	var init proto.InitUploadResponse
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return proto.Item{}, fmt.Errorf("init upload: %s: %s", resp.Status, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&init); err != nil {
		resp.Body.Close()
		return proto.Item{}, err
	}
	resp.Body.Close()

	chunkSize := init.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}

	// 2. chunks
	var offset int64
	for offset < total {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		n := end - offset
		if _, err := f.Seek(offset, 0); err != nil {
			_ = a.abortUpload(ctx, init.UploadID)
			return proto.Item{}, err
		}
		body := io.LimitReader(f, n)
		q := url.Values{}
		q.Set("offset", fmt.Sprintf("%d", offset))
		path := "/api/uploads/" + init.UploadID + "/chunk?" + q.Encode()
		pr := &progressReader{r: body, total: total, fn: progress, done: offset}
		req, err := a.authReq(ctx, http.MethodPut, path, pr)
		if err != nil {
			_ = a.abortUpload(ctx, init.UploadID)
			return proto.Item{}, err
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := a.http.Do(req)
		if err != nil {
			_ = a.abortUpload(ctx, init.UploadID)
			return proto.Item{}, fmt.Errorf("chunk @%d: %w", offset, err)
		}
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			_ = a.abortUpload(ctx, init.UploadID)
			return proto.Item{}, fmt.Errorf("chunk @%d: %s: %s", offset, resp.Status, b)
		}
		// Drain + close so the connection can be reused.
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		offset = end
		if progress != nil {
			progress(offset, total)
		}
	}

	// 3. finalize
	final, err := a.authReq(ctx, http.MethodPost, "/api/uploads/"+init.UploadID+"/finalize", nil)
	if err != nil {
		return proto.Item{}, err
	}
	resp2, err := a.http.Do(final)
	if err != nil {
		return proto.Item{}, err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp2.Body, 4096))
		return proto.Item{}, fmt.Errorf("finalize: %s: %s", resp2.Status, b)
	}
	var it proto.Item
	if err := json.NewDecoder(resp2.Body).Decode(&it); err != nil {
		return proto.Item{}, err
	}
	return it, nil
}

// abortUpload tells the server to discard a partially uploaded session.
func (a *API) abortUpload(ctx context.Context, uploadID string) error {
	r, err := a.authReq(ctx, http.MethodDelete, "/api/uploads/"+uploadID, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (a *API) List(ctx context.Context) ([]proto.Item, error) {
	r, err := a.authReq(ctx, http.MethodGet, "/api/items", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list: %s", resp.Status)
	}
	var lr proto.ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	return lr.Items, nil
}

// GetText fetches the full text content of a text item.
func (a *API) GetText(ctx context.Context, id string) (string, error) {
	r, err := a.authReq(ctx, http.MethodGet, "/api/items/"+id+"/content", nil)
	if err != nil {
		return "", err
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("get text: %s", resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// Update patches an item. Pass non-nil pointers for the fields you want to
// change; nil leaves them alone. Server replies with the updated item.
func (a *API) Update(ctx context.Context, id string, upd proto.UpdateItemRequest) (proto.Item, error) {
	body, _ := json.Marshal(upd)
	r, err := a.authReq(ctx, http.MethodPatch, "/api/items/"+id, bytes.NewReader(body))
	if err != nil {
		return proto.Item{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(r)
	if err != nil {
		return proto.Item{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return proto.Item{}, fmt.Errorf("update: %s: %s", resp.Status, b)
	}
	var it proto.Item
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
		return proto.Item{}, err
	}
	return it, nil
}

// Delete removes an item from the server. Server publishes EventDeleted to
// every connected client, so the UI updates from the websocket stream.
func (a *API) Delete(ctx context.Context, id string) error {
	r, err := a.authReq(ctx, http.MethodDelete, "/api/items/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete: %s: %s", resp.Status, b)
	}
	return nil
}

// DownloadFile streams a file item to destPath with progress callbacks.
func (a *API) DownloadFile(ctx context.Context, id, destPath string, progress ProgressFunc) error {
	r, err := a.authReq(ctx, http.MethodGet, "/api/items/"+id+"/content", nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	pw := &progressWriter{w: f, total: resp.ContentLength, fn: progress}
	_, err = io.Copy(pw, resp.Body)
	return err
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	last  time.Time
	fn    ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil && (err != nil || time.Since(p.last) > 100*time.Millisecond) {
		p.last = time.Now()
		p.fn(p.done, p.total)
	}
	return n, err
}

type progressWriter struct {
	w     io.Writer
	done  int64
	total int64
	last  time.Time
	fn    ProgressFunc
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.fn != nil && (err != nil || time.Since(p.last) > 100*time.Millisecond) {
		p.last = time.Now()
		p.fn(p.done, p.total)
	}
	return n, err
}
