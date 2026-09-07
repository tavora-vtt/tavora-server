package asset_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/core/asset"
)

func gradient(width, height int) image.Image {
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 80, A: 255})
		}
	}
	return canvas
}

func encodePNG(t *testing.T, source image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, source); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buffer.Bytes()
}

func TestNormalizeReencodesAndHashesTheResult(t *testing.T) {
	raw := encodePNG(t, gradient(64, 40))

	normalized, err := asset.Normalize(raw, asset.Limits{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	if normalized.Mime != asset.MimePNG {
		t.Errorf("mime is %q, want %q", normalized.Mime, asset.MimePNG)
	}
	if normalized.Width != 64 || normalized.Height != 40 {
		t.Errorf("size is %dx%d, want 64x40", normalized.Width, normalized.Height)
	}
	if len(normalized.SHA256) != 64 {
		t.Errorf("hash is %q, want 64 hex characters", normalized.SHA256)
	}
	if normalized.Key != normalized.SHA256 {
		t.Errorf("key %q does not address the content", normalized.Key)
	}
	if normalized.Bytes != int64(len(normalized.Content)) {
		t.Errorf("recorded %d bytes for %d", normalized.Bytes, len(normalized.Content))
	}
	if _, err := png.Decode(bytes.NewReader(normalized.Content)); err != nil {
		t.Errorf("the re-encoded content is not a png: %v", err)
	}
}

func TestTheSamePixelsAlwaysProduceTheSameHash(t *testing.T) {
	raw := encodePNG(t, gradient(32, 32))

	first, err := asset.Normalize(raw, asset.Limits{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	second, err := asset.Normalize(raw, asset.Limits{})
	if err != nil {
		t.Fatalf("normalize again: %v", err)
	}

	if first.SHA256 != second.SHA256 {
		t.Errorf("the same upload hashed to %s and %s", first.SHA256, second.SHA256)
	}
}

func TestMetadataDoesNotSurviveNormalization(t *testing.T) {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, gradient(48, 48), nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}

	secret := []byte("GPS 51.2277 6.7735")
	withComment := append([]byte{0xff, 0xd8, 0xff, 0xfe, 0x00, byte(len(secret) + 2)}, secret...)
	withComment = append(withComment, buffer.Bytes()[2:]...)

	normalized, err := asset.Normalize(withComment, asset.Limits{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if normalized.Mime != asset.MimeJPEG {
		t.Errorf("mime is %q, want %q", normalized.Mime, asset.MimeJPEG)
	}
	if bytes.Contains(normalized.Content, secret) {
		t.Error("the comment segment survived re-encoding")
	}
}

func TestSVGIsRejected(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	if _, err := asset.Normalize(svg, asset.Limits{}); !errors.Is(err, asset.ErrUnsupportedFormat) {
		t.Fatalf("normalize accepted svg: %v", err)
	}
}

func TestAFileNamedLikeAnImageIsStillJudgedByItsBytes(t *testing.T) {
	if _, err := asset.Normalize([]byte("#!/bin/sh\nrm -rf /\n"), asset.Limits{}); !errors.Is(err, asset.ErrUnsupportedFormat) {
		t.Fatalf("normalize accepted a shell script: %v", err)
	}
}

func TestOversizedUploadsAreRefusedBeforeDecoding(t *testing.T) {
	raw := encodePNG(t, gradient(64, 64))

	if _, err := asset.Normalize(raw, asset.Limits{MaxBytes: 8}); !errors.Is(err, asset.ErrTooLarge) {
		t.Fatalf("normalize accepted an oversized upload: %v", err)
	}
	if _, err := asset.Normalize(raw, asset.Limits{MaxPixels: 16}); !errors.Is(err, asset.ErrTooLarge) {
		t.Fatalf("normalize accepted an oversized image: %v", err)
	}
}

func TestTruncatedImagesAreRefused(t *testing.T) {
	raw := encodePNG(t, gradient(64, 64))

	if _, err := asset.Normalize(raw[:len(raw)/2], asset.Limits{}); !errors.Is(err, asset.ErrBroken) {
		t.Fatalf("normalize accepted a truncated png: %v", err)
	}
}

func TestThumbnailFitsTheRequestedBox(t *testing.T) {
	raw := encodePNG(t, gradient(800, 400))

	normalized, err := asset.Normalize(raw, asset.Limits{Thumbnail: 100})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	if normalized.Thumbnail.Width != 100 || normalized.Thumbnail.Height != 50 {
		t.Errorf("thumbnail is %dx%d, want 100x50",
			normalized.Thumbnail.Width, normalized.Thumbnail.Height)
	}
	if normalized.Thumbnail.Key != normalized.SHA256+"-thumb" {
		t.Errorf("thumbnail key is %q", normalized.Thumbnail.Key)
	}
	if normalized.Thumbnail.Bytes >= normalized.Bytes {
		t.Errorf("the thumbnail weighs %d against the full image at %d",
			normalized.Thumbnail.Bytes, normalized.Bytes)
	}
}

func TestSmallImagesKeepTheirSize(t *testing.T) {
	raw := encodePNG(t, gradient(40, 20))

	normalized, err := asset.Normalize(raw, asset.Limits{Thumbnail: 256})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if normalized.Thumbnail.Width != 40 || normalized.Thumbnail.Height != 20 {
		t.Errorf("thumbnail is %dx%d, want the original 40x20",
			normalized.Thumbnail.Width, normalized.Thumbnail.Height)
	}
}
