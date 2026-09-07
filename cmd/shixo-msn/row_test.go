package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/sherifhamad/shixo-msn/internal/proto"
)

// newRow must hand back the *historyRow itself, and that row must render its
// box. If either breaks, updateRow silently no-ops (blank list) or someone
// reintroduces the map[fyne.CanvasObject]*historyRow that leaked a full widget
// tree on every list Refresh.
func TestNewRowRoundTrips(t *testing.T) {
	test.NewApp()
	u := &ui{}

	var o fyne.CanvasObject = u.newRow()
	r, ok := o.(*historyRow)
	if !ok {
		t.Fatalf("newRow returned %T, want *historyRow", o)
	}

	objs := test.WidgetRenderer(r).Objects()
	if len(objs) != 1 || objs[0] != fyne.CanvasObject(r.box) {
		t.Fatalf("renderer objects = %v, want just the row box", objs)
	}
}

func TestItemMatches(t *testing.T) {
	it := proto.Item{Title: "Deploy Notes", Folder: "Ops", Source: "Laptop", Text: "TOKEN=abc"}
	for _, tc := range []struct {
		q    string
		want bool
	}{
		{"", true},
		{"deploy", true},  // title, case-insensitive
		{"ops", true},     // folder
		{"laptop", true},  // source
		{"token=abc", true}, // body
		{"nope", false},
	} {
		if got := itemMatches(it, tc.q); got != tc.want {
			t.Errorf("itemMatches(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
	if !itemMatches(proto.Item{Filename: "report.pdf"}, "report") {
		t.Error("filename should match")
	}
}
