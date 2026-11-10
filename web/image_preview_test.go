package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aristath/songya"
)

func TestImagePreviewsPreserveOriginalsAndRemainLocalOnFailure(t *testing.T) {
	_, decoderErr := exec.LookPath("ffmpeg")
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := exec.LookPath(filepath.Join(home, ".local", "lib", "songya", "bin", "ffmpeg")); err == nil {
			decoderErr = nil
		}
	}
	if decoderErr != nil {
		t.Skip("FFmpeg is required for image previews")
	}
	img := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{30, 80, 200, 255})
		}
	}
	var input bytes.Buffer
	_ = png.Encode(&input, img)
	original := input.Bytes()
	store, _ := songya.Open(t.TempDir())
	doc, _ := store.Create(songya.Entities, map[string]any{"name": "Mary"}, "Keep these words.")
	upload, err := store.Upload(context.Background(), "photo", "test", "photo.png", bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ChangeFile(context.Background(), songya.FileChange{Key: "attach", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	path := "/api/records/" + doc.ID + "/files/photo.png"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("GET", path+"?preview=1&thumbnail=1", nil))
	preview, err := png.Decode(bytes.NewReader(response.Body.Bytes()))
	if response.Code != 200 || err != nil || preview.Bounds().Dx() != 512 {
		t.Fatal(response.Code, err, response.Body.String())
	}
	request := httptest.NewRequest("GET", path+"?preview=1&thumbnail=1", nil)
	request.Header.Set("If-None-Match", response.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	app.ServeHTTP(cached, request)
	if cached.Code != 304 {
		t.Fatal(cached.Code)
	}
	download := httptest.NewRecorder()
	app.ServeHTTP(download, httptest.NewRequest("GET", path, nil))
	if !bytes.Equal(download.Body.Bytes(), original) {
		t.Fatal("Original image bytes changed")
	}
	// Hide both the PATH decoder and Songya's packaged decoder for this request.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	broken := httptest.NewRecorder()
	app.ServeHTTP(broken, httptest.NewRequest("GET", path+"?preview=1&thumbnail=1", nil))
	if broken.Code != 422 {
		t.Fatal(broken.Code)
	}
	read, _ := store.Read(doc.ID)
	if read.Body != "Keep these words." {
		t.Fatal(read.Body)
	}
	listing, err := store.Attachments(context.Background(), doc.ID)
	if err != nil || len(listing.Files) != 1 || listing.Files[0].Preview != "image" {
		t.Fatal(listing, err)
	}
}
