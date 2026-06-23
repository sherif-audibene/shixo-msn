package server

import (
	"errors"
	"sync"
	"time"
)

// RecommendedChunkSize is what we suggest to clients. Cloudflare Tunnel /
// Cloudflare proxy free tier caps request body at ~100MB, so 50MB chunks
// leave room for headers and slack.
const RecommendedChunkSize int64 = 50 * 1024 * 1024

// UploadTTL is how long an idle upload session survives before garbage
// collection sweeps its on-disk staging dir.
const UploadTTL = 6 * time.Hour

// uploadSession tracks an in-progress chunked upload.
type uploadSession struct {
	ID       string
	Filename string
	Source   string
	Title    string
	Folder   string
	Size     int64  // declared total size
	Received int64  // bytes appended so far
	Created  time.Time
	Touched  time.Time
}

// Uploads keeps the active sessions in memory. A long-running server will
// also have leftover dirs on disk after restart; those don't auto-resume
// (clients should re-upload) but a periodic sweep removes stale dirs.
type Uploads struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
}

func NewUploads() *Uploads {
	return &Uploads{sessions: map[string]*uploadSession{}}
}

func (u *Uploads) New(id, filename, source, title, folder string, size int64) *uploadSession {
	s := &uploadSession{
		ID: id, Filename: filename, Source: source, Title: title, Folder: folder, Size: size,
		Created: time.Now(), Touched: time.Now(),
	}
	u.mu.Lock()
	u.sessions[id] = s
	u.mu.Unlock()
	return s
}

func (u *Uploads) Get(id string) (*uploadSession, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.sessions[id]
	return s, ok
}

func (u *Uploads) Touch(id string, addReceived int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.sessions[id]
	if !ok {
		return errors.New("unknown upload id")
	}
	s.Received += addReceived
	s.Touched = time.Now()
	return nil
}

func (u *Uploads) Drop(id string) {
	u.mu.Lock()
	delete(u.sessions, id)
	u.mu.Unlock()
}

// SweepStale removes idle sessions older than UploadTTL and returns their IDs
// so the caller can also clean their on-disk staging dirs.
func (u *Uploads) SweepStale() []string {
	cutoff := time.Now().Add(-UploadTTL)
	var out []string
	u.mu.Lock()
	for id, s := range u.sessions {
		if s.Touched.Before(cutoff) {
			out = append(out, id)
			delete(u.sessions, id)
		}
	}
	u.mu.Unlock()
	return out
}
