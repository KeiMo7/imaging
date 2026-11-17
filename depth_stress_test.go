package imaging

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestTenBitLongRunMemoryAndCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("long-run bit-depth test")
	}
	src := makeTenBitGradient(128, 96)
	dir := t.TempDir()
	name := filepath.Join(dir, "thumb.png")

	for i := 0; i < 5; i++ {
		if err := Save10(src, name); err != nil {
			t.Fatalf("warmup save: %v", err)
		}
	}
	runtime.GC()
	beforeGoroutines := runtime.NumGoroutine()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for i := 0; i < 100; i++ {
		thumb, err := ThumbnailWithDepth(src, 64, 48, Lanczos)
		if err != nil {
			t.Fatalf("iteration %d thumbnail: %v", i, err)
		}
		if err := Save10(thumb, name); err != nil {
			t.Fatalf("iteration %d save: %v", i, err)
		}
		if i%10 == 0 {
			failName := filepath.Join(dir, fmt.Sprintf("fail-%d.jpg", i))
			if err := Save10(thumb, failName); err == nil {
				t.Fatalf("iteration %d expected unsupported format error", i)
			}
			assertNoTempFiles(t, dir)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > 16<<20 {
		t.Fatalf("heap grew by %d bytes across long run", growth)
	}
	if got := runtime.NumGoroutine(); got > beforeGoroutines+2 {
		t.Fatalf("goroutines grew from %d to %d", beforeGoroutines, got)
	}
	assertNoTempFiles(t, dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "thumb.png" {
		t.Fatalf("directory contains unexpected files: %v", entries)
	}
}

func TestTenBitConcurrentProcessing(t *testing.T) {
	src := makeTenBitGradient(96, 64)
	dir := t.TempDir()

	const workers = 8
	const iterations = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*iterations)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				dst, err := ResizeWithDepth(src, 48, 32, Lanczos)
				if err != nil {
					errs <- err
					continue
				}
				if _, ok := dst.(*image.NRGBA64); !ok {
					errs <- fmt.Errorf("worker %d output type %T", worker, dst)
					continue
				}
				name := filepath.Join(dir, fmt.Sprintf("%d-%d.png", worker, i))
				if err := Save10(dst, name); err != nil {
					errs <- err
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != workers*iterations {
		t.Fatalf("output count = %d, want %d", len(entries), workers*iterations)
	}
	assertNoTempFiles(t, dir)
}
