package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io/ioutil"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchQualityQualityFirst(t *testing.T) {
	var calls int
	encode := func(quality int) ([]byte, error) {
		calls++
		return make([]byte, quality*100), nil
	}

	result, err := SearchQuality(550, encode, QualitySearchConfig{})
	if err != nil {
		t.Fatalf("SearchQuality: %v", err)
	}
	if result.Quality != 5 || result.Bytes != 500 || !result.Achieved {
		t.Fatalf("got quality=%d bytes=%d achieved=%v, want quality=5 bytes=500 achieved=true",
			result.Quality, result.Bytes, result.Achieved)
	}
	if calls != result.Iterations || result.Iterations != len(result.Attempts) {
		t.Fatalf("got calls=%d iterations=%d attempts=%d", calls, result.Iterations, len(result.Attempts))
	}

	wantQualities := []int{100, 1, 50, 25, 13, 7, 4, 5, 6}
	gotQualities := attemptQualities(result.Attempts)
	if !reflect.DeepEqual(gotQualities, wantQualities) {
		t.Fatalf("got trace %v, want %v", gotQualities, wantQualities)
	}
	for _, attempt := range result.Attempts {
		if attempt.Bytes <= 0 {
			t.Fatalf("attempt %d has no measured size: %#v", attempt.Iteration, attempt)
		}
	}

	repeated, err := SearchQuality(550, func(quality int) ([]byte, error) {
		return make([]byte, quality*100), nil
	}, QualitySearchConfig{})
	if err != nil {
		t.Fatalf("repeated SearchQuality: %v", err)
	}
	if repeated.Quality != result.Quality ||
		!reflect.DeepEqual(attemptQualities(repeated.Attempts), gotQualities) {
		t.Fatalf("search is not deterministic: first=%v repeated=%v",
			gotQualities, attemptQualities(repeated.Attempts))
	}
}

func TestSearchQualitySizeFirst(t *testing.T) {
	encode := func(quality int) ([]byte, error) {
		return make([]byte, quality*100), nil
	}

	result, err := SearchQuality(550, encode, QualitySearchConfig{Mode: SizeFirst})
	if err != nil {
		t.Fatalf("SearchQuality: %v", err)
	}
	if result.Quality != 1 || result.Bytes != 100 || result.Iterations != 1 {
		t.Fatalf("got quality=%d bytes=%d iterations=%d", result.Quality, result.Bytes, result.Iterations)
	}

	result, err = SearchQuality(50, encode, QualitySearchConfig{Mode: SizeFirst})
	if !errors.Is(err, ErrTargetSizeUnreachable) {
		t.Fatalf("got error %v, want ErrTargetSizeUnreachable", err)
	}
	if result == nil || result.MinimumBytes != 100 || result.Achieved {
		t.Fatalf("got result %#v", result)
	}
}

func TestSearchQualityIterationLimitReturnsClosest(t *testing.T) {
	result, err := SearchQuality(4999, func(quality int) ([]byte, error) {
		return make([]byte, quality*100), nil
	}, QualitySearchConfig{MaxIterations: 3})
	if !errors.Is(err, ErrTargetSizeIterationLimit) {
		t.Fatalf("got error %v, want ErrTargetSizeIterationLimit", err)
	}
	if result == nil {
		t.Fatal("expected the closest result")
	}
	if result.Achieved {
		t.Fatal("iteration-limited result must not be marked achieved")
	}
	if result.Iterations != 3 || result.Quality != 50 || result.Bytes != 5000 {
		t.Fatalf("got iterations=%d quality=%d bytes=%d, want 3, 50, 5000",
			result.Iterations, result.Quality, result.Bytes)
	}
}

func TestSearchQualityEncoderErrorIsNotRetried(t *testing.T) {
	encodeErr := errors.New("encoder failed")
	var calls int
	result, err := SearchQuality(1000, func(quality int) ([]byte, error) {
		calls++
		return nil, encodeErr
	}, QualitySearchConfig{})
	if err != encodeErr {
		t.Fatalf("got error %v, want the encoder error itself", err)
	}
	if calls != 1 {
		t.Fatalf("encoder called %d times, want 1", calls)
	}
	if result == nil || len(result.Attempts) != 1 || result.Attempts[0].Error == "" {
		t.Fatalf("expected one traced failed attempt, got %#v", result)
	}
}

func TestOptimizeToTargetSizeSmallImagePassthrough(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(8, 6), PNG)
	var calls int32
	encoder := NewTargetSizeEncoder(WithTargetSizeImageEncoder(func(img image.Image, quality int) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("must not encode")
	}))

	result, err := encoder.OptimizeToTargetSize(source, int64(len(source)+100))
	if err != nil {
		t.Fatalf("OptimizeToTargetSize: %v", err)
	}
	if !result.Unchanged || !result.Achieved || result.Iterations != 0 {
		t.Fatalf("got unchanged=%v achieved=%v iterations=%d",
			result.Unchanged, result.Achieved, result.Iterations)
	}
	if !bytes.Equal(result.Data, source) {
		t.Fatal("small image was not returned byte-for-byte")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("encoder called %d times, want 0", atomic.LoadInt32(&calls))
	}
}

func TestOptimizeToTargetSizeLosslessUnreachable(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(64, 64), PNG)
	encoder := NewTargetSizeEncoder()

	result, err := encoder.OptimizeToTargetSize(source, 1)
	if !errors.Is(err, ErrTargetSizeUnreachable) {
		t.Fatalf("got error %v, want ErrTargetSizeUnreachable", err)
	}
	if result == nil {
		t.Fatal("expected an unreachable result with a minimum estimate")
	}
	if result.Achieved || result.Unchanged {
		t.Fatalf("unreachable result marked achieved=%v unchanged=%v", result.Achieved, result.Unchanged)
	}
	if result.Format != PNG {
		t.Fatalf("got output format %v, want PNG (no lossy downgrade)", result.Format)
	}
	if result.MinimumBytes <= 1 || result.Bytes != result.MinimumBytes {
		t.Fatalf("got minimum=%d bytes=%d", result.MinimumBytes, result.Bytes)
	}
	if len(result.Attempts) != 4 {
		t.Fatalf("got %d lossless attempts, want 4", len(result.Attempts))
	}
	if bytes.HasPrefix(result.Data, []byte{0xff, 0xd8}) {
		t.Fatal("lossless search returned JPEG data")
	}
}

func TestOptimizeToTargetSizeLosslessIterationLimit(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(64, 64), PNG)
	result, err := NewTargetSizeEncoder().OptimizeToTargetSize(
		source, 1, TargetSizeMaxIterations(2),
	)
	if !errors.Is(err, ErrTargetSizeIterationLimit) {
		t.Fatalf("got error %v, want ErrTargetSizeIterationLimit", err)
	}
	if result == nil || result.Achieved || result.Iterations != 2 {
		t.Fatalf("got result %#v", result)
	}
	if result.MinimumBytes <= 1 {
		t.Fatalf("got minimum estimate %d", result.MinimumBytes)
	}
}

func TestOptimizeToTargetSizeLossySearch(t *testing.T) {
	img := targetSizeGradient(96, 64)
	source := mustEncodeTargetImage(t, img, JPEG, JPEGQuality(100))
	minimumData := mustEncodeTargetImage(t, img, JPEG, JPEGQuality(1))
	target := int64((len(source) + len(minimumData)) / 2)
	if target >= int64(len(source)) {
		t.Fatal("test target must require re-encoding")
	}

	encoder := NewTargetSizeEncoder()
	result, err := encoder.OptimizeToTargetSize(source, target)
	if err != nil {
		t.Fatalf("OptimizeToTargetSize: %v", err)
	}
	if !result.Achieved || result.Bytes > target {
		t.Fatalf("got achieved=%v bytes=%d target=%d", result.Achieved, result.Bytes, target)
	}
	if result.Quality <= 1 || result.Quality >= 100 {
		t.Fatalf("got quality=%d, expected an interior value", result.Quality)
	}
	if result.Iterations == 0 || result.Iterations != len(result.Attempts) {
		t.Fatalf("got iterations=%d attempts=%d", result.Iterations, len(result.Attempts))
	}
	if result.MinimumBytes != int64(len(minimumData)) {
		t.Fatalf("got minimum=%d, want %d", result.MinimumBytes, len(minimumData))
	}
	decoded, format, err := image.Decode(bytes.NewReader(result.Data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if format != "jpeg" || decoded.Bounds() != img.Bounds() {
		t.Fatalf("got format=%q bounds=%v, want jpeg %v", format, decoded.Bounds(), img.Bounds())
	}

	sizeFirst, err := NewTargetSizeEncoder().OptimizeToTargetSize(
		source, target, TargetSizeSearchMode(SizeFirst),
	)
	if err != nil {
		t.Fatalf("size-first OptimizeToTargetSize: %v", err)
	}
	if sizeFirst.Quality != 1 || sizeFirst.Bytes != int64(len(minimumData)) {
		t.Fatalf("size-first got quality=%d bytes=%d, want quality=1 bytes=%d",
			sizeFirst.Quality, sizeFirst.Bytes, len(minimumData))
	}
}

func TestOptimizeToTargetSizeAlphaJPEG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0xff, A: 0})
		}
	}
	source := mustEncodeTargetImage(t, img, PNG)
	target := int64(len(mustEncodeTargetImage(t, flattenOnBackground(
		img, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	), JPEG, JPEGQuality(100))))

	result, err := NewTargetSizeEncoder().OptimizeToTargetSize(
		source, target, TargetSizeFormat(JPEG),
	)
	if err != nil {
		t.Fatalf("OptimizeToTargetSize: %v", err)
	}
	if !result.Alpha || !result.AlphaFlattened {
		t.Fatalf("got alpha=%v flattened=%v, want both true", result.Alpha, result.AlphaFlattened)
	}
	decoded, _, err := image.Decode(bytes.NewReader(result.Data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	r, g, b, _ := decoded.At(8, 8).RGBA()
	if r < 0xef00 || g < 0xef00 || b < 0xef00 {
		t.Fatalf("transparent pixel was not flattened to white: %#x %#x %#x", r, g, b)
	}
}

func TestOptimizeToTargetSizePathological(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 0x40, G: 0x80, B: 0xc0, A: 0xff})
	source := mustEncodeTargetImage(t, img, JPEG, JPEGQuality(100))

	result, err := NewTargetSizeEncoder().OptimizeToTargetSize(source, 1)
	if !errors.Is(err, ErrTargetSizeUnreachable) {
		t.Fatalf("got error %v, want ErrTargetSizeUnreachable", err)
	}
	if result == nil || result.MinimumBytes <= 1 || result.Achieved {
		t.Fatalf("got result %#v", result)
	}
}

func TestOptimizeToTargetSizeExtremeAspectRatio(t *testing.T) {
	img := targetSizeGradient(1, 1024)
	source := mustEncodeTargetImage(t, img, PNG)
	target := int64(len(mustEncodeTargetImage(t, img, JPEG, JPEGQuality(100))))

	result, err := NewTargetSizeEncoder().OptimizeToTargetSize(
		source, target, TargetSizeFormat(JPEG),
	)
	if err != nil {
		t.Fatalf("OptimizeToTargetSize: %v", err)
	}
	if !result.Achieved || result.Bytes > target {
		t.Fatalf("got achieved=%v bytes=%d target=%d", result.Achieved, result.Bytes, target)
	}
	decoded, _, err := image.Decode(bytes.NewReader(result.Data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Bounds() != image.Rect(0, 0, 1, 1024) {
		t.Fatalf("got bounds %v", decoded.Bounds())
	}
}

func TestTargetSizeCacheHit(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(8, 8), PNG)
	var calls int32
	encoder := NewTargetSizeEncoder(WithTargetSizeImageEncoder(func(img image.Image, quality int) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return make([]byte, 1000+quality*10), nil
	}))
	options := []TargetSizeOption{TargetSizeFormat(JPEG)}

	first, err := encoder.OptimizeToTargetSize(source, 1500, options...)
	if err != nil {
		t.Fatalf("first OptimizeToTargetSize: %v", err)
	}
	firstCalls := atomic.LoadInt32(&calls)
	if first.Iterations == 0 || first.Iterations != int(firstCalls) {
		t.Fatalf("got iterations=%d encoder calls=%d", first.Iterations, firstCalls)
	}

	second, err := encoder.OptimizeToTargetSize(source, 1500, options...)
	if err != nil {
		t.Fatalf("cached OptimizeToTargetSize: %v", err)
	}
	if !second.Cached || second.Merged || second.Iterations != 0 {
		t.Fatalf("got cached=%v merged=%v iterations=%d", second.Cached, second.Merged, second.Iterations)
	}
	if atomic.LoadInt32(&calls) != firstCalls {
		t.Fatalf("cache hit encoded again: before=%d after=%d", firstCalls, atomic.LoadInt32(&calls))
	}
	if !bytes.Equal(first.Data, second.Data) || first.Quality != second.Quality {
		t.Fatal("cache hit returned different data or quality")
	}
}

func TestTargetSizeConcurrentSameTargetMerged(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(8, 8), PNG)
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	encoder := NewTargetSizeEncoder(WithTargetSizeImageEncoder(func(img image.Image, quality int) ([]byte, error) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			once.Do(func() { close(started) })
			<-release
		}
		return make([]byte, 1000+quality*10), nil
	}))
	options := []TargetSizeOption{TargetSizeFormat(JPEG)}

	type outcome struct {
		result *TargetSizeResult
		err    error
	}
	firstCh := make(chan outcome, 1)
	go func() {
		result, err := encoder.OptimizeToTargetSize(source, 1500, options...)
		firstCh <- outcome{result: result, err: err}
	}()
	<-started

	secondCh := make(chan outcome, 1)
	go func() {
		result, err := encoder.OptimizeToTargetSize(source, 1500, options...)
		secondCh <- outcome{result: result, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)

	first := <-firstCh
	second := <-secondCh
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent errors: first=%v second=%v", first.err, second.err)
	}
	mergedCount := 0
	ownerIterations := 0
	for _, outcome := range []outcome{first, second} {
		if outcome.result.Merged {
			mergedCount++
			if outcome.result.Iterations != 0 {
				t.Fatalf("merged result performed %d iterations", outcome.result.Iterations)
			}
		} else {
			ownerIterations = outcome.result.Iterations
		}
	}
	if mergedCount != 1 {
		t.Fatalf("got %d merged results, want 1", mergedCount)
	}
	if int(atomic.LoadInt32(&calls)) != ownerIterations {
		t.Fatalf("encoder called %d times for one search with %d iterations",
			atomic.LoadInt32(&calls), ownerIterations)
	}
}

func TestTargetSizeConcurrentDifferentTargetsParallel(t *testing.T) {
	source := mustEncodeTargetImage(t, targetSizeGradient(8, 8), PNG)
	var calls int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	encoder := NewTargetSizeEncoder(WithTargetSizeImageEncoder(func(img image.Image, quality int) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return make([]byte, 1000+quality*10), nil
	}))
	options := []TargetSizeOption{TargetSizeFormat(JPEG)}

	type outcome struct {
		result *TargetSizeResult
		err    error
	}
	results := make(chan outcome, 2)
	go func() {
		result, err := encoder.OptimizeToTargetSize(source, 2500, options...)
		results <- outcome{result: result, err: err}
	}()
	go func() {
		result, err := encoder.OptimizeToTargetSize(source, 2600, options...)
		results <- outcome{result: result, err: err}
	}()

	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-timeout:
			close(release)
			t.Fatal("different targets did not encode in parallel")
		}
	}
	close(release)

	for i := 0; i < 2; i++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("concurrent search: %v", outcome.err)
		}
		if outcome.result.Merged || outcome.result.Cached || outcome.result.Iterations != 1 {
			t.Fatalf("got merged=%v cached=%v iterations=%d",
				outcome.result.Merged, outcome.result.Cached, outcome.result.Iterations)
		}
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("encoder called %d times, want 2", atomic.LoadInt32(&calls))
	}
}

func TestTargetSizeAcceptanceRealImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance test in short mode")
	}
	data, err := ioutil.ReadFile("testdata/branches.jpg")
	if err != nil {
		t.Fatalf("read acceptance image: %v", err)
	}

	for _, target := range []int64{120000, 80000, 40000} {
		encoder := NewTargetSizeEncoder()
		result, err := encoder.OptimizeToTargetSize(data, target, TargetSizeFormat(JPEG))
		if err != nil {
			t.Fatalf("target %d: %v", target, err)
		}
		t.Logf(
			"target=%d final=%d quality=%d iterations=%d elapsed=%s trace=%v",
			target, result.Bytes, result.Quality, result.Iterations,
			result.Duration.Round(time.Microsecond), attemptSummary(result.Attempts),
		)
	}
}

func TestTargetSizeAcceptanceConcurrentRealImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance test in short mode")
	}
	data, err := ioutil.ReadFile("testdata/branches.jpg")
	if err != nil {
		t.Fatalf("read acceptance image: %v", err)
	}
	options := []TargetSizeOption{TargetSizeFormat(JPEG)}

	type outcome struct {
		target int64
		result *TargetSizeResult
		err    error
	}

	sameEncoder := NewTargetSizeEncoder()
	start := time.Now()
	results := make(chan outcome, 2)
	go func() {
		result, err := sameEncoder.OptimizeToTargetSize(data, 90000, options...)
		results <- outcome{target: 90000, result: result, err: err}
	}()
	go func() {
		result, err := sameEncoder.OptimizeToTargetSize(data, 90000, options...)
		results <- outcome{target: 90000, result: result, err: err}
	}()

	var sameTargetOwnerIterations int
	var sameTargetMerged int
	for i := 0; i < 2; i++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("same target %d: %v", outcome.target, outcome.err)
		}
		if outcome.result.Merged || outcome.result.Cached {
			sameTargetMerged++
		} else {
			sameTargetOwnerIterations = outcome.result.Iterations
		}
		t.Logf(
			"same-target concurrent target=%d final=%d quality=%d iterations=%d elapsed=%s merged=%v cached=%v",
			outcome.target, outcome.result.Bytes, outcome.result.Quality,
			outcome.result.Iterations, outcome.result.Duration.Round(time.Microsecond),
			outcome.result.Merged, outcome.result.Cached,
		)
	}
	if sameTargetMerged != 1 || sameTargetOwnerIterations == 0 {
		t.Fatalf("got merged=%d owner_iterations=%d", sameTargetMerged, sameTargetOwnerIterations)
	}
	t.Logf(
		"same target merged: total_elapsed=%s total_encodes=%d",
		time.Since(start).Round(time.Microsecond), sameTargetOwnerIterations,
	)

	encoder := NewTargetSizeEncoder()
	start = time.Now()
	results = make(chan outcome, 2)
	go func() {
		result, err := encoder.OptimizeToTargetSize(data, 100000, options...)
		results <- outcome{target: 100000, result: result, err: err}
	}()
	go func() {
		result, err := encoder.OptimizeToTargetSize(data, 50000, options...)
		results <- outcome{target: 50000, result: result, err: err}
	}()

	var totalIterations int
	for i := 0; i < 2; i++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("target %d: %v", outcome.target, outcome.err)
		}
		totalIterations += outcome.result.Iterations
		t.Logf(
			"concurrent target=%d final=%d quality=%d iterations=%d elapsed=%s merged=%v cached=%v",
			outcome.target, outcome.result.Bytes, outcome.result.Quality,
			outcome.result.Iterations, outcome.result.Duration.Round(time.Microsecond),
			outcome.result.Merged, outcome.result.Cached,
		)
	}
	t.Logf(
		"two different targets: total_elapsed=%s total_encodes=%d",
		time.Since(start).Round(time.Microsecond), totalIterations,
	)

	start = time.Now()
	sequentialIterations := 0
	for _, target := range []int64{100000, 50000} {
		result, err := NewTargetSizeEncoder().OptimizeToTargetSize(data, target, options...)
		if err != nil {
			t.Fatalf("sequential target %d: %v", target, err)
		}
		sequentialIterations += result.Iterations
	}
	t.Logf(
		"two targets sequential baseline: total_elapsed=%s total_encodes=%d",
		time.Since(start).Round(time.Microsecond), sequentialIterations,
	)
}

func attemptQualities(attempts []QualitySearchAttempt) []int {
	qualities := make([]int, len(attempts))
	for i, attempt := range attempts {
		qualities[i] = attempt.Quality
	}
	return qualities
}

func attemptSummary(attempts []TargetSizeAttempt) string {
	var buf bytes.Buffer
	for i, attempt := range attempts {
		if i > 0 {
			buf.WriteByte(' ')
		}
		fmt.Fprintf(&buf, "%d:%d/%d", attempt.Iteration, attempt.Quality, attempt.Bytes)
	}
	return buf.String()
}

func mustEncodeTargetImage(t *testing.T, img image.Image, format Format, opts ...EncodeOption) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, img, format, opts...); err != nil {
		t.Fatalf("encode %v: %v", format, err)
	}
	return buf.Bytes()
}

func targetSizeGradient(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	widthDenominator := width - 1
	if widthDenominator < 1 {
		widthDenominator = 1
	}
	heightDenominator := height - 1
	if heightDenominator < 1 {
		heightDenominator = 1
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / widthDenominator),
				G: uint8(y * 255 / heightDenominator),
				B: uint8((x + y) * 255 / (widthDenominator + heightDenominator)),
				A: 0xff,
			})
		}
	}
	return img
}
