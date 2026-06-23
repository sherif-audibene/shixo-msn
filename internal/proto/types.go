// Package proto holds wire types shared between server and client.
package proto

import "time"

// Kind distinguishes a text snippet from an uploaded file.
type Kind string

const (
	KindText Kind = "text"
	KindFile Kind = "file"
)

// Item is one entry in the shared history.
type Item struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Source    string    `json:"source"`              // hostname of origin
	CreatedAt time.Time `json:"created_at"`
	Title     string    `json:"title,omitempty"`     // user-supplied label (optional)
	Folder    string    `json:"folder,omitempty"`    // user-defined group, "" == uncategorized
	Text      string    `json:"text,omitempty"`      // populated when Kind == text
	Filename  string    `json:"filename,omitempty"`  // populated when Kind == file
	Size      int64     `json:"size,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	MIME      string    `json:"mime,omitempty"`
}

// PushTextRequest is the body of POST /api/items/text.
type PushTextRequest struct {
	Title  string `json:"title,omitempty"`
	Folder string `json:"folder,omitempty"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

// InitUploadRequest opens a new chunked upload session.
type InitUploadRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Source   string `json:"source"`
	Title    string `json:"title,omitempty"`
	Folder   string `json:"folder,omitempty"`
}

// UpdateItemRequest is the body of PATCH /api/items/:id.
// Fields are pointers so the caller can distinguish "unset" from "set to empty".
type UpdateItemRequest struct {
	Title  *string `json:"title,omitempty"`
	Folder *string `json:"folder,omitempty"`
	Text   *string `json:"text,omitempty"` // text items only; ignored for files
}

// InitUploadResponse holds the upload id used for subsequent chunk PUTs.
type InitUploadResponse struct {
	UploadID  string `json:"upload_id"`
	ChunkSize int64  `json:"chunk_size"` // server's recommended size
}

// ListResponse is the response body of GET /api/items.
type ListResponse struct {
	Items []Item `json:"items"`
}

// Event types pushed over the WebSocket.
const (
	EventNewItem = "new_item"
	EventUpdated = "updated"
	EventDeleted = "deleted"
)

// Event is a server-to-client websocket message.
type Event struct {
	Type string `json:"type"`
	Item *Item  `json:"item,omitempty"`
	ID   string `json:"id,omitempty"`
}
