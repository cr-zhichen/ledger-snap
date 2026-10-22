package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ledger-snap/internal/ocr"
)

// countingOCR returns lines and err after delay, and counts the calls.
type countingOCR struct {
	lines []ocr.Line
	err   error
	delay time.Duration
	calls int
}

func (e *countingOCR) Name() string { return "fake" }

func (e *countingOCR) Recognize(context.Context, []byte) ([]ocr.Line, error) {
	e.calls++
	time.Sleep(e.delay)
	return e.lines, e.err
}

func TestCachedOCR(t *testing.T) {
	ctx := context.Background()
	image := []byte("jpeg")

	t.Run("no cache", func(t *testing.T) {
		engine := &countingOCR{lines: []ocr.Line{{Text: "支付成功"}}}
		// nil lets Recognize run OCR itself.
		if lines, _, err := cachedOCR(ctx, engine, "", "01_pay.jpg", image); lines != nil || err != nil || engine.calls != 0 {
			t.Fatalf("got %v, %v after %d calls", lines, err, engine.calls)
		}
	})

	t.Run("miss then hit", func(t *testing.T) {
		dir := t.TempDir()
		want := []ocr.Line{{Text: "支付成功", X: 1, Y: 2, W: 3, H: 4, Prob: 0.9}, {Text: "¥25.00", Y: 50, H: 30}}
		engine := &countingOCR{lines: want, delay: 20 * time.Millisecond}
		for i := 0; i < 2; i++ {
			lines, took, err := cachedOCR(ctx, engine, dir, "01_pay.jpg", image)
			if err != nil || !reflect.DeepEqual(lines, want) {
				t.Fatalf("call %d: got %v, %v", i+1, lines, err)
			}
			// The miss reports the time OCR took, the hit none.
			if miss := i == 0; miss != (took >= engine.delay) {
				t.Errorf("call %d took %v", i+1, took)
			}
		}
		if engine.calls != 1 {
			t.Errorf("OCR ran %d times, want once", engine.calls)
		}
		// The engine name keeps providers apart.
		if _, err := os.Stat(filepath.Join(dir, "fake_01_pay.json")); err != nil {
			t.Error(err)
		}
	})

	t.Run("no text is cached too", func(t *testing.T) {
		dir := t.TempDir()
		engine := &countingOCR{}
		for i := 0; i < 2; i++ {
			// Empty, not nil: nil would make Recognize run OCR again.
			if lines, _, err := cachedOCR(ctx, engine, dir, "05_blank.jpg", image); lines == nil || len(lines) != 0 || err != nil {
				t.Fatalf("call %d: got %#v, %v", i+1, lines, err)
			}
		}
		if engine.calls != 1 {
			t.Errorf("OCR ran %d times, want once", engine.calls)
		}
	})

	t.Run("OCR error is not cached", func(t *testing.T) {
		dir := t.TempDir()
		engine := &countingOCR{err: errors.New("connection refused")}
		if _, _, err := cachedOCR(ctx, engine, dir, "01_pay.jpg", image); err == nil {
			t.Fatal("want the OCR error")
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("cached a failure: %v", entries)
		}
		engine.err, engine.lines = nil, []ocr.Line{{Text: "支付成功"}}
		if lines, _, err := cachedOCR(ctx, engine, dir, "01_pay.jpg", image); err != nil || len(lines) != 1 || engine.calls != 2 {
			t.Errorf("retry: got %v, %v after %d calls", lines, err, engine.calls)
		}
	})

	t.Run("broken cache file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fake_01_pay.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		engine := &countingOCR{lines: []ocr.Line{{Text: "支付成功"}}}
		if _, _, err := cachedOCR(ctx, engine, dir, "01_pay.jpg", image); err == nil || engine.calls != 0 {
			t.Errorf("got %v after %d calls, want an error without OCR", err, engine.calls)
		}
	})
}
