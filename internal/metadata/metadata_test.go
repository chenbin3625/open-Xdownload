package metadata

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestWriteSidecarAndEmbedJPEGAndPNG(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(2, 2, color.RGBA{R: 255, G: 128, B: 64, A: 255})

	tweet := parser.TweetData{
		ID:        "12345",
		URL:       "https://x.com/alice/status/12345",
		Text:      "Hello <world> & X",
		CreatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		Author:    parser.Author{ID: "42", Name: "Alice", ScreenName: "alice"},
	}
	media := parser.Media{ID: "m1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/abc.jpg", AltText: "A sunset"}

	jpgPath := filepath.Join(dir, "sample.jpg")
	f, err := os.Create(jpgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := WriteSidecarAndEmbed(jpgPath, tweet, media); err != nil {
		t.Fatalf("JPEG WriteSidecarAndEmbed: %v", err)
	}
	rawJPG, err := os.ReadFile(jpgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawJPG, []byte("http://ns.adobe.com/xap/1.0/")) || !bytes.Contains(rawJPG, []byte("x-tweet:12345")) {
		t.Fatal("JPEG missing embedded XMP")
	}
	if _, err := jpeg.Decode(bytes.NewReader(rawJPG)); err != nil {
		t.Fatalf("embedded JPEG no longer decodable: %v", err)
	}
	sidecarBytes, err := os.ReadFile(jpgPath + ".json")
	if err != nil {
		t.Fatalf("missing JSON sidecar: %v", err)
	}
	var sc Sidecar
	if err := json.Unmarshal(sidecarBytes, &sc); err != nil || sc.TweetID != "12345" || sc.AltText != "A sunset" {
		t.Fatalf("unexpected sidecar: %+v err=%v", sc, err)
	}

	pngPath := filepath.Join(dir, "sample.png")
	pf, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(pf, img); err != nil {
		t.Fatal(err)
	}
	_ = pf.Close()
	if err := WriteSidecarAndEmbed(pngPath, tweet, media); err != nil {
		t.Fatalf("PNG WriteSidecarAndEmbed: %v", err)
	}
	rawPNG, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawPNG, []byte("XML:com.adobe.xmp")) {
		t.Fatal("PNG missing embedded XMP chunk")
	}
	if _, err := png.Decode(bytes.NewReader(rawPNG)); err != nil {
		t.Fatalf("embedded PNG no longer decodable: %v", err)
	}
}
