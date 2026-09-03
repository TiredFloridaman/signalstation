package main

import (
	"context"
	"errors"
	"image"
	"image/color"
	"sort"
	"strings"
	"time"

	"github.com/kbinani/screenshot"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// ErrNoQRFound means no Signal linking QR code was visible on any display.
var ErrNoQRFound = errors.New("no Signal linking QR code found on screen")

// ErrNoDisplays means the screen capture API reported nothing to capture, which
// on macOS almost always means Screen Recording permission has not been granted.
var ErrNoDisplays = errors.New("no displays available to capture")

// scanBudget caps the work spent on one display per poll. Exceeding it is not a
// failure: the watcher simply tries again on the next tick, by which point the
// window may have moved or finished drawing.
const scanBudget = 3 * time.Second

// IsLinkURI and normalizeLinkText are defined in ui_link.go, alongside the
// linking UI that is their only consumer. qrscan.go uses IsLinkURI too, but
// keeping the definitions next to the caller that pastes untrusted text makes
// the build resilient to a stale copy of either file.

// ScanScreensForLinkURI grabs every display and hunts for a Signal linking QR
// code, returning the URI encoded in it.
//
// A QR code inside a Signal Desktop window is a small patch of a very large
// screenshot, and decoders do poorly on that: the finder patterns are a tiny
// fraction of the frame. So the image is also swept in overlapping tiles, which
// puts the QR at a workable relative size in at least one of them.
func ScanScreensForLinkURI() (string, error) {
	n := screenshot.NumActiveDisplays()
	if n <= 0 {
		return "", ErrNoDisplays
	}

	for i := 0; i < n; i++ {
		bounds := screenshot.GetDisplayBounds(i)
		img, err := screenshot.CaptureRect(bounds)
		if err != nil {
			continue // a display may fail (locked, mirrored); try the others
		}
		if uri, ok := findLinkURI(img, scanBudget); ok {
			return uri, nil
		}
	}
	return "", ErrNoQRFound
}

// findLinkURI tries the whole frame, then overlapping tiles at several sizes,
// then repeats both against an inverted copy.
//
// Inversion matters because QR decoders expect dark modules on a light
// background. A dark-themed window can render the code the other way round, and
// a decoder that only tries one polarity silently finds nothing.
//
// Multiple tile sizes matter because the right crop depends on how large the QR
// is relative to the display: a small code on a 4K panel needs a tight tile,
// while a large one on a 1080p panel gets cut in half by that same tile.
func findLinkURI(img image.Image, budget time.Duration) (string, bool) {
	deadline := time.Now().Add(budget)

	for _, variant := range []image.Image{img, invertImage(img)} {
		if variant == nil {
			continue
		}
		if text, ok := decodeQR(variant); ok && IsLinkURI(text) {
			return strings.TrimSpace(text), true
		}
		if uri, ok := sweepTiles(variant, deadline); ok {
			return uri, true
		}
		if time.Now().After(deadline) {
			return "", false
		}
	}
	return "", false
}

// sweepTiles walks the frame in overlapping crops.
//
// With a step of half the tile, any target up to half a tile wide is guaranteed
// to sit inside some crop. Codes larger than that are left to the full-frame
// decode above, which is precisely the size range where it succeeds — so the
// two passes cover between them without needing an expensive fine-grained grid.
//
// Crops are visited nearest the centre of the screen first, because an
// application window showing a QR code is rarely in a corner, and the sweep
// stops at the first hit.
func sweepTiles(img image.Image, deadline time.Time) (string, bool) {
	type cropper interface {
		SubImage(r image.Rectangle) image.Image
	}
	sub, ok := img.(cropper)
	if !ok {
		return "", false
	}
	b := img.Bounds()
	cx, cy := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2

	for _, tile := range []int{720, 448} {
		step := tile / 2

		var rects []image.Rectangle
		for y := b.Min.Y; y < b.Max.Y; y += step {
			for x := b.Min.X; x < b.Max.X; x += step {
				r := image.Rect(x, y, min(x+tile, b.Max.X), min(y+tile, b.Max.Y))
				if r.Dx() < 120 || r.Dy() < 120 {
					continue
				}
				rects = append(rects, r)
			}
		}
		sort.Slice(rects, func(i, j int) bool {
			return centreDist(rects[i], cx, cy) < centreDist(rects[j], cx, cy)
		})

		for _, r := range rects {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return "", false // out of budget; the next poll picks up again
			}
			if text, ok := decodeQR(sub.SubImage(r)); ok && IsLinkURI(text) {
				return strings.TrimSpace(text), true
			}
		}
	}
	return "", false
}

func centreDist(r image.Rectangle, cx, cy int) int {
	dx := (r.Min.X+r.Max.X)/2 - cx
	dy := (r.Min.Y+r.Max.Y)/2 - cy
	return dx*dx + dy*dy
}

// invertImage returns a colour-inverted copy, preserving SubImage support so the
// tile sweep can crop it.
func invertImage(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: 255 - uint8(r>>8),
				G: 255 - uint8(g>>8),
				B: 255 - uint8(bl>>8),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// decodeQR runs one decode attempt with TRY_HARDER enabled, which allows the
// decoder to consider rotated and lower-contrast candidates at some CPU cost.
func decodeQR(img image.Image) (string, bool) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", false
	}
	hints := map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_TRY_HARDER: true,
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, hints)
	if err != nil {
		return "", false
	}
	return res.GetText(), true
}

// WatchForLinkURI polls the screen until a linking QR appears or ctx is done.
// This lets the user press one button and then go about scanning, rather than
// having to time a click against Signal Desktop finishing its startup.
func WatchForLinkURI(ctx context.Context, interval time.Duration) (string, error) {
	// Fail fast on the permission problem rather than silently polling forever.
	if screenshot.NumActiveDisplays() <= 0 {
		return "", ErrNoDisplays
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		uri, err := ScanScreensForLinkURI()
		if err == nil {
			return uri, nil
		}
		if errors.Is(err, ErrNoDisplays) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ErrNoQRFound
		case <-ticker.C:
		}
	}
}
