package imaging

import (
	"fmt"
	"image"
	"image/color"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBatchMemoryScaling(t *testing.T) {
	if os.Getenv("IMAGING_BATCH_MEMORY") == "" {
		t.Skip("set IMAGING_BATCH_MEMORY=1 to run the memory scaling measurement")
	}

	dir, err := ioutil.TempDir("", "imaging-batch-memory")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	const (
		total  = 6
		width  = 1600
		height = 1200
	)
	inputs := make([]BatchInput, 0, total)
	for i := 0; i < total; i++ {
		src := filepath.Join(dir, fmt.Sprintf("memory-%d.png", i))
		writeSolidImage(t, src, width, height, blendMemoryColor(i))
		inputs = append(inputs, BatchInput{
			Source:      src,
			Destination: filepath.Join(dir, fmt.Sprintf("memory-out-%d.jpg", i)),
		})
	}

	params := BatchParams{
		Process: func(src image.Image) (*image.NRGBA, error) {
			dst := Clone(src)
			time.Sleep(40 * time.Millisecond)
			return dst, nil
		},
	}
	for _, concurrency := range []int{1, 2, 4} {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		stop := make(chan struct{})
		sampled := make(chan uint64)
		go func() {
			var peak uint64
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					var stats runtime.MemStats
					runtime.ReadMemStats(&stats)
					if stats.HeapAlloc > peak {
						peak = stats.HeapAlloc
					}
				case <-stop:
					sampled <- peak
					return
				}
			}
		}()

		result, err := ProcessBatch(inputs, params, BatchOptions{
			Concurrency:   concurrency,
			BatchSize:     total,
			MaxBatchBytes: 1 << 30,
		})
		close(stop)
		peak := <-sampled
		if err != nil {
			t.Fatalf("concurrency %d: %v", concurrency, err)
		}
		if len(result.Failed) != 0 {
			t.Fatalf("concurrency %d: %d items failed", concurrency, len(result.Failed))
		}

		delta := uint64(0)
		if peak > before.HeapAlloc {
			delta = peak - before.HeapAlloc
		}
		t.Logf("concurrency=%d peak_heap_delta=%.1fMiB total_heap_peak=%.1fMiB",
			concurrency,
			float64(delta)/(1<<20),
			float64(peak)/(1<<20),
		)
	}
}

func blendMemoryColor(i int) color.NRGBA {
	return color.NRGBA{
		R: uint8(40 + i*25),
		G: uint8(200 - i*15),
		B: uint8(80 + i*10),
		A: 255,
	}
}
