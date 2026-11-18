package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchMixedInputs(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	red := filepath.Join(dir, "red.png")
	blue := filepath.Join(dir, "blue.png")
	large := filepath.Join(dir, "large.jpg")
	writeSolidImage(t, red, 320, 240, color.NRGBA{R: 255, A: 255})
	writeSolidImage(t, blue, 48, 32, color.NRGBA{B: 255, A: 255})
	writeSolidImage(t, large, 2400, 1800, color.NRGBA{G: 160, A: 255})

	broken := filepath.Join(dir, "broken.png")
	if err := ioutil.WriteFile(broken, []byte("not an image"), 0644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.png")

	inputs := []BatchInput{
		{Source: red, Destination: filepath.Join(dir, "out-red.png")},
		{Source: missing, Destination: filepath.Join(dir, "out-missing.png")},
		{Source: blue, Destination: filepath.Join(dir, "out-blue.png")},
		{Source: broken, Destination: filepath.Join(dir, "out-broken.png")},
		{Source: large, Destination: filepath.Join(dir, "out-large.jpg")},
	}

	var progress []BatchProgress
	result, err := ProcessBatch(
		inputs,
		BatchParams{
			Operation: BatchThumbnail,
			Width:     64,
			Height:    64,
			Filter:    Lanczos,
		},
		BatchOptions{
			Concurrency:   3,
			BatchSize:     4,
			MaxBatchBytes: 1,
			Progress: func(p BatchProgress) {
				progress = append(progress, p)
			},
		},
	)
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}

	if len(result.Items) != len(inputs) {
		t.Fatalf("got %d items, want %d", len(result.Items), len(inputs))
	}
	for i, item := range result.Items {
		if item.Index != i || item.Source != inputs[i].Source || item.Destination != inputs[i].Destination {
			t.Fatalf("item %d is out of order or has wrong metadata: %#v", i, item)
		}
	}

	wantStatus := []BatchStatus{
		BatchStatusSucceeded,
		BatchStatusFailed,
		BatchStatusSucceeded,
		BatchStatusFailed,
		BatchStatusSucceeded,
	}
	for i, want := range wantStatus {
		if result.Items[i].Status != want {
			t.Fatalf("item %d status = %d, want %d (err=%v)", i, result.Items[i].Status, want, result.Items[i].Err)
		}
	}
	if len(result.Succeeded) != 3 || len(result.Failed) != 2 {
		t.Fatalf("got %d succeeded and %d failed, want 3 and 2", len(result.Succeeded), len(result.Failed))
	}
	if !strings.Contains(result.Items[1].Err.Error(), missing) {
		t.Fatalf("missing-file error does not identify %q: %v", missing, result.Items[1].Err)
	}
	if !strings.Contains(result.Items[3].Err.Error(), broken) {
		t.Fatalf("decode error does not identify %q: %v", broken, result.Items[3].Err)
	}

	for _, item := range result.Succeeded {
		if _, err := os.Stat(item.Destination); err != nil {
			t.Fatalf("successful item did not produce %q: %v", item.Destination, err)
		}
	}
	if len(progress) == 0 {
		t.Fatal("progress callback was not called")
	}
	final := progress[len(progress)-1]
	if final.Completed != len(inputs) || final.Failed != 2 || final.Active != 0 {
		t.Fatalf("unexpected final progress: %#v", final)
	}
	if final.Batches < 2 || final.Batches > len(inputs) {
		t.Fatalf("got %d batches, want size-based splitting into 2..%d batches", final.Batches, len(inputs))
	}
	for _, p := range progress {
		if p.Concurrency > 3 || p.Active > 3 {
			t.Fatalf("concurrency exceeded limit: %#v", p)
		}
	}

	retry := result.RetryInputs()
	if len(retry) != 2 || retry[0].Source != missing || retry[1].Source != broken {
		t.Fatalf("unexpected retry inputs: %#v", retry)
	}

	failureList := filepath.Join(dir, "failures.json")
	if err := result.WriteFailureList(failureList); err != nil {
		t.Fatalf("WriteFailureList failed: %v", err)
	}
	data, err := ioutil.ReadFile(failureList)
	if err != nil {
		t.Fatal(err)
	}
	var records []BatchFailure
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("failure list is not valid JSON: %v\n%s", err, data)
	}
	if len(records) != 2 || records[0].Source != missing || records[1].Source != broken {
		t.Fatalf("unexpected failure records: %#v", records)
	}

	output, err := Open(result.Items[0].Destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := output.Bounds().Size(); got != image.Pt(64, 64) {
		t.Fatalf("output size = %v, want 64x64", got)
	}

	t.Logf("batch summary: total=%d succeeded=%d failed=%d batches=%d final=%+v",
		len(result.Items), len(result.Succeeded), len(result.Failed), final.Batches, final)
}

func TestBatchPreservesInputOrderWithConcurrentCompletion(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	const total = 12
	inputs := make([]BatchInput, 0, total)
	for i := 0; i < total; i++ {
		path := filepath.Join(dir, fmt.Sprintf("in-%02d.png", i))
		writeSolidImage(t, path, 8, 8, color.NRGBA{R: uint8(i * 20), A: 255})
		inputs = append(inputs, BatchInput{
			Source:      path,
			Destination: filepath.Join(dir, fmt.Sprintf("out-%02d.png", i)),
		})
	}

	var active int32
	var maxActive int32
	var observedProgress []BatchProgress
	result, err := ProcessBatch(
		inputs,
		BatchParams{
			Process: func(src image.Image) (*image.NRGBA, error) {
				current := atomic.AddInt32(&active, 1)
				for {
					previous := atomic.LoadInt32(&maxActive)
					if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
						break
					}
				}
				c := color.NRGBAModel.Convert(src.At(0, 0)).(color.NRGBA)
				time.Sleep(time.Duration(total-int(c.R/20)) * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return Clone(src), nil
			},
		},
		BatchOptions{
			Concurrency: 4,
			BatchSize:   total,
			Progress: func(p BatchProgress) {
				observedProgress = append(observedProgress, p)
			},
		},
	)
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}
	for i, item := range result.Items {
		if item.Index != i || item.Source != inputs[i].Source || item.Status != BatchStatusSucceeded {
			t.Fatalf("item %d = %#v, want source %q", i, item, inputs[i].Source)
		}
	}
	if got := atomic.LoadInt32(&maxActive); got != 4 {
		t.Fatalf("max active = %d, want 4", got)
	}
	for _, p := range observedProgress {
		if p.Active > p.Concurrency {
			t.Fatalf("progress active exceeds concurrency: %#v", p)
		}
	}
}

func TestBatchValidateOnceAndConcurrencyFallback(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	inputs := make([]BatchInput, 0, 5)
	for i := 0; i < 5; i++ {
		src := filepath.Join(dir, fmt.Sprintf("in-%d.png", i))
		writeSolidImage(t, src, 4, 4, color.NRGBA{R: 20, A: 255})
		inputs = append(inputs, BatchInput{
			Source:      src,
			Destination: filepath.Join(dir, fmt.Sprintf("out-%d.png", i)),
		})
	}

	var validations int32
	var processed int32
	var progress []BatchProgress
	result, err := ProcessBatch(
		inputs,
		BatchParams{
			Validate: func() error {
				atomic.AddInt32(&validations, 1)
				return nil
			},
			Process: func(src image.Image) (*image.NRGBA, error) {
				atomic.AddInt32(&processed, 1)
				return Clone(src), nil
			},
		},
		BatchOptions{
			Concurrency:   0,
			BatchSize:     -1,
			MaxBatchBytes: 0,
			Progress: func(p BatchProgress) {
				progress = append(progress, p)
			},
		},
	)
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}
	if got := atomic.LoadInt32(&validations); got != 1 {
		t.Fatalf("validator called %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&processed); got != int32(len(inputs)) {
		t.Fatalf("processed %d items, want %d", got, len(inputs))
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("got warnings %v, want 2 fallback warnings", result.Warnings)
	}
	if len(progress) == 0 || progress[0].Concurrency <= 0 || progress[0].Concurrency > DefaultBatchConcurrency {
		t.Fatalf("invalid concurrency was not replaced: %#v", progress)
	}

	_, err = ProcessBatchAsync(
		inputs,
		BatchParams{
			Validate: func() error { return errors.New("bad shared parameters") },
			Process: func(src image.Image) (*image.NRGBA, error) {
				return Clone(src), nil
			},
		},
		BatchOptions{Concurrency: 2},
	)
	if !errors.Is(err, ErrInvalidBatchParams) {
		t.Fatalf("got error %v, want ErrInvalidBatchParams", err)
	}
}

func TestBatchCancelLetsRunningItemsFinish(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	const total = 8
	inputs := make([]BatchInput, 0, total)
	for i := 0; i < total; i++ {
		src := filepath.Join(dir, fmt.Sprintf("in-%d.png", i))
		writeSolidImage(t, src, 4, 4, color.NRGBA{R: 10, A: 255})
		inputs = append(inputs, BatchInput{
			Source:      src,
			Destination: filepath.Join(dir, fmt.Sprintf("out-%d.png", i)),
		})
	}

	started := make(chan struct{}, total)
	release := make(chan struct{})
	var running int32
	var finished int32
	job, err := ProcessBatchAsync(
		inputs,
		BatchParams{
			Process: func(src image.Image) (*image.NRGBA, error) {
				atomic.AddInt32(&running, 1)
				started <- struct{}{}
				<-release
				atomic.AddInt32(&finished, 1)
				atomic.AddInt32(&running, -1)
				return Clone(src), nil
			},
		},
		BatchOptions{Concurrency: 2, BatchSize: total},
	)
	if err != nil {
		t.Fatalf("ProcessBatchAsync failed: %v", err)
	}

	<-started
	<-started
	job.Cancel()
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&running); got != 2 {
		t.Fatalf("running after cancel = %d, want 2", got)
	}

	close(release)
	result := job.Wait()
	if !result.WasCanceled {
		t.Fatal("result was not marked as canceled")
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("got %d successful items, want 2", len(result.Succeeded))
	}
	if len(result.Canceled) != total-2 {
		t.Fatalf("got %d canceled items, want %d", len(result.Canceled), total-2)
	}
	if got := atomic.LoadInt32(&finished); got != 2 {
		t.Fatalf("finished %d items, want exactly the 2 already running", got)
	}
	for _, item := range result.Canceled {
		if item.Status != BatchStatusCanceled || !errors.Is(item.Err, ErrBatchCanceled) {
			t.Fatalf("unexpected canceled item: %#v", item)
		}
	}
	final := job.Progress()
	if final.Completed != total || final.Succeeded != 2 || final.Canceled != total-2 || final.Active != 0 {
		t.Fatalf("unexpected final progress: %#v", final)
	}
}

func TestBatchContextCancellation(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "in.png")
	writeSolidImage(t, src, 8, 8, color.NRGBA{B: 200, A: 255})
	inputs := []BatchInput{
		{Source: src, Destination: filepath.Join(dir, "out.png")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := ProcessBatchContext(ctx, inputs, BatchParams{
		Operation: BatchOriginal,
	}, BatchOptions{Concurrency: 1})
	if err != nil {
		t.Fatalf("ProcessBatchContext failed: %v", err)
	}
	if !result.WasCanceled || len(result.Canceled) != 1 {
		t.Fatalf("unexpected canceled result: %#v", result)
	}
}

func TestBatchSharedBufferIsolation(t *testing.T) {
	dir := batchTempDir(t)
	defer os.RemoveAll(dir)
	colors := []color.NRGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
		{R: 80, G: 160, B: 240, A: 255},
		{R: 240, G: 80, B: 160, A: 255},
		{R: 160, G: 240, B: 80, A: 255},
	}
	inputs := make([]BatchInput, 0, len(colors))
	for i, c := range colors {
		src := filepath.Join(dir, fmt.Sprintf("solid-%d.png", i))
		dst := filepath.Join(dir, fmt.Sprintf("solid-out-%d.png", i))
		writeSolidImage(t, src, 32, 24, c)
		inputs = append(inputs, BatchInput{Source: src, Destination: dst})
	}

	result, err := ProcessBatch(inputs, BatchParams{
		Operation: BatchResize,
		Width:     16,
		Height:    12,
		Filter:    Linear,
	}, BatchOptions{Concurrency: 3})
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}
	for i, item := range result.Items {
		if item.Status != BatchStatusSucceeded {
			t.Fatalf("item %d failed: %v", i, item.Err)
		}
		img, err := Open(item.Destination)
		if err != nil {
			t.Fatal(err)
		}
		got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA)
		want := colors[i]
		if !colorsClose(got, want, 1) {
			t.Fatalf("output %d got color %#v, want %#v", i, got, want)
		}
	}
}

func TestBatchInvalidParams(t *testing.T) {
	cases := []BatchParams{
		{Operation: BatchResize},
		{Operation: BatchFit, Width: 0, Height: 10},
		{Operation: BatchThumbnail, Width: 10, Height: 10, Anchor: Anchor(99)},
		{Operation: BatchOperation(99), Width: 10, Height: 10},
		{Operation: BatchResize, Width: 10, Filter: ResampleFilter{Support: 1}},
	}
	for i, params := range cases {
		_, err := ProcessBatchAsync(nil, params, BatchOptions{Concurrency: 1})
		if !errors.Is(err, ErrInvalidBatchParams) {
			t.Fatalf("case %d: got error %v, want ErrInvalidBatchParams", i, err)
		}
	}
}

func writeSolidImage(t *testing.T, filename string, width, height int, c color.NRGBA) {
	t.Helper()
	img := New(width, height, c)
	if err := Save(img, filename); err != nil {
		t.Fatalf("failed to create %q: %v", filename, err)
	}
}

func batchTempDir(t *testing.T) string {
	t.Helper()
	dir, err := ioutil.TempDir("", "imaging-batch")
	if err != nil {
		t.Fatalf("failed to create temporary directory: %v", err)
	}
	return dir
}

func colorsClose(a, b color.NRGBA, delta int) bool {
	diff := func(x, y uint8) bool {
		d := int(x) - int(y)
		if d < 0 {
			d = -d
		}
		return d <= delta
	}
	return diff(a.R, b.R) && diff(a.G, b.G) && diff(a.B, b.B) && diff(a.A, b.A)
}
