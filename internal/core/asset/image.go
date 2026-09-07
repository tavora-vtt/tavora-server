package asset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	_ "image/gif"
)

var (
	ErrUnsupportedFormat = errors.New("asset: unsupported format")
	ErrTooLarge          = errors.New("asset: too large")
	ErrBroken            = errors.New("asset: cannot be decoded")
)

const (
	MimePNG  = "image/png"
	MimeJPEG = "image/jpeg"

	jpegQuality = 88

	// DefaultMaxBytes is what a single upload may weigh before re-encoding.
	DefaultMaxBytes = 32 << 20
	// DefaultMaxPixels caps decoded area, which is what actually costs memory.
	DefaultMaxPixels = 8192 * 8192
	// DefaultThumbnail is the longest edge of the preview the picker shows.
	DefaultThumbnail = 256
)

// Limits bound an upload before and after decoding. Zero fields fall back to the
// defaults.
type Limits struct {
	MaxBytes  int64
	MaxPixels int
	Thumbnail int
}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxPixels <= 0 {
		l.MaxPixels = DefaultMaxPixels
	}
	if l.Thumbnail <= 0 {
		l.Thumbnail = DefaultThumbnail
	}
	return l
}

// Variant is one encoded rendition of an upload, addressed by the hash of its own bytes.
type Variant struct {
	Content []byte `json:"-"`
	Key     string `json:"key"`
	Mime    string `json:"mime"`
	Bytes   int64  `json:"bytes"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// Normalized is an upload after it has been decoded and written out again, which is what
// strips metadata and neutralizes files that are valid in two formats at once.
type Normalized struct {
	Variant
	SHA256    string
	Thumbnail Variant
}

// Normalize decodes an upload and encodes it again from pixels. The result never shares
// bytes with the input, so nothing the uploader smuggled in survives.
func Normalize(raw []byte, limits Limits) (*Normalized, error) {
	limits = limits.withDefaults()

	if int64(len(raw)) > limits.MaxBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(raw))
	}

	format, err := sniff(raw)
	if err != nil {
		return nil, err
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	if config.Width*config.Height > limits.MaxPixels {
		return nil, fmt.Errorf("%w: %d by %d pixels", ErrTooLarge, config.Width, config.Height)
	}

	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}

	full, err := encode(decoded, format)
	if err != nil {
		return nil, err
	}

	thumbnail, err := encode(scaleToFit(decoded, limits.Thumbnail), MimePNG)
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(full.Content)
	sum := hex.EncodeToString(digest[:])

	full.Key = sum
	thumbnail.Key = sum + "-thumb"

	return &Normalized{Variant: full, SHA256: sum, Thumbnail: thumbnail}, nil
}

// sniff decides the format from magic bytes. The filename and the client supplied content
// type never take part in the decision.
func sniff(raw []byte) (string, error) {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return MimePNG, nil
	case bytes.HasPrefix(raw, []byte("\xff\xd8\xff")):
		return MimeJPEG, nil
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return MimePNG, nil
	default:
		return "", ErrUnsupportedFormat
	}
}

func encode(source image.Image, mime string) (Variant, error) {
	bounds := source.Bounds()
	variant := Variant{
		Mime:   mime,
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
	}

	var buffer bytes.Buffer
	var err error
	switch mime {
	case MimeJPEG:
		err = jpeg.Encode(&buffer, source, &jpeg.Options{Quality: jpegQuality})
	default:
		variant.Mime = MimePNG
		err = png.Encode(&buffer, source)
	}
	if err != nil {
		return Variant{}, fmt.Errorf("asset: encode: %w", err)
	}

	variant.Content = buffer.Bytes()
	variant.Bytes = int64(buffer.Len())
	return variant, nil
}

// scaleToFit shrinks an image so its longest edge is at most size, averaging over the
// source pixels that fall into each target pixel. Images already small enough are copied
// rather than returned as they are, so the thumbnail never aliases the full rendition.
func scaleToFit(source image.Image, size int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	scale := 1.0
	if longest := max(width, height); longest > size {
		scale = float64(size) / float64(longest)
	}

	target := image.NewRGBA(image.Rect(0, 0,
		max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))))
	if scale == 1 {
		draw.Draw(target, target.Bounds(), source, bounds.Min, draw.Src)
		return target
	}

	stepX := float64(width) / float64(target.Bounds().Dx())
	stepY := float64(height) / float64(target.Bounds().Dy())

	for y := range target.Bounds().Dy() {
		for x := range target.Bounds().Dx() {
			target.Set(x, y, average(source,
				bounds.Min.X+int(float64(x)*stepX), bounds.Min.X+int(float64(x+1)*stepX),
				bounds.Min.Y+int(float64(y)*stepY), bounds.Min.Y+int(float64(y+1)*stepY)))
		}
	}
	return target
}

func average(source image.Image, left, right, top, bottom int) color.RGBA {
	right = max(right, left+1)
	bottom = max(bottom, top+1)

	var red, green, blue, alpha, count uint64
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			r, g, b, a := source.At(x, y).RGBA()
			red += uint64(r)
			green += uint64(g)
			blue += uint64(b)
			alpha += uint64(a)
			count++
		}
	}
	if count == 0 {
		return color.RGBA{}
	}

	return color.RGBA{
		R: uint8(red / count >> 8),
		G: uint8(green / count >> 8),
		B: uint8(blue / count >> 8),
		A: uint8(alpha / count >> 8),
	}
}
