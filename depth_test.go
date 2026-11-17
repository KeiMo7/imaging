package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
)

func TestTenBitPassthrough(t *testing.T) {
	src := makeTenBitGradient(128, 32)
	cloned, err := CloneWithDepth(src)
	if err != nil {
		t.Fatalf("CloneWithDepth: %v", err)
	}
	cloned64 := cloned.(*image.NRGBA64)
	if !bytes.Equal(cloned64.Pix, src.Pix) {
		t.Fatal("10-bit clone altered source samples")
	}
	dst, err := ResizeWithDepth(src, 64, 16, Lanczos)
	if err != nil {
		t.Fatalf("ResizeWithDepth: %v", err)
	}
	out, ok := dst.(*image.NRGBA64)
	if !ok {
		t.Fatalf("output type = %T, want *image.NRGBA64", dst)
	}
	if depth := DetectBitDepth(out); depth != BitDepth10 {
		t.Fatalf("output depth = %s, want 10", depth)
	}

	unique := make(map[uint16]struct{})
	hasHighSample := false
	row := make([]uint16, out.Bounds().Dx()*4)
	srcScanner := newScanner64(out)
	for y := 0; y < out.Bounds().Dy(); y++ {
		srcScanner.scan(0, y, out.Bounds().Dx(), y+1, row)
		for _, v := range row {
			if !is10BitSample(v) {
				t.Fatalf("sample 0x%04x is not 10-bit aligned", v)
			}
			if v > 0x00ff {
				hasHighSample = true
			}
			unique[v>>6] = struct{}{}
		}
	}
	if !hasHighSample {
		t.Fatal("10-bit passthrough collapsed all samples to 8-bit range")
	}
	if len(unique) < 256 {
		t.Fatalf("only %d 10-bit levels survived; expected at least 256", len(unique))
	}
	t.Logf("10-bit resize retained %d levels", len(unique))
}

func TestEightBitToTenBitDither(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 256, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 256; x++ {
			i := y*src.Stride + x*4
			src.Pix[i+0] = uint8(x)
			src.Pix[i+1] = uint8(x)
			src.Pix[i+2] = uint8(x)
			src.Pix[i+3] = 0xff
		}
	}

	plain, err := ConvertToDepth(src, WithBitDepth(BitDepth10))
	if err != nil {
		t.Fatalf("ConvertToDepth: %v", err)
	}
	plain64 := plain.(*image.NRGBA64)
	dither, err := ConvertToDepth(src, WithBitDepth(BitDepth10), WithDither(DitherOrdered))
	if err != nil {
		t.Fatalf("ConvertToDepth with dither: %v", err)
	}
	dither64 := dither.(*image.NRGBA64)

	plainSamples := nrgba64Samples(plain64)
	ditherSamples := nrgba64Samples(dither64)
	if len(plainSamples) != len(ditherSamples) {
		t.Fatal("sample count mismatch")
	}
	for x := 0; x < 256; x++ {
		got := uint16(plain64.Pix[x*8])<<8 | uint16(plain64.Pix[x*8+1])
		gotQ := uint16(got >> 6)
		wantQ := uint16((x*1023 + 127) / 255)
		if gotQ != wantQ {
			t.Fatalf("x=%d 8-to-10 value = %d, want %d", x, gotQ, wantQ)
		}
	}
	ditherChanged := false
	for i := range plainSamples {
		if plainSamples[i] != ditherSamples[i] {
			ditherChanged = true
		}
		if plainSamples[i]>>6 > ditherSamples[i]>>6+1 ||
			ditherSamples[i]>>6 > plainSamples[i]>>6+1 {
			t.Fatalf("dither changed sample too far: plain=0x%04x dither=0x%04x",
				plainSamples[i], ditherSamples[i])
		}
	}
	if !ditherChanged {
		t.Fatal("ordered dither did not change any sample")
	}

}

func TestMixedDepthBatchResizesPerItem(t *testing.T) {
	src8 := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for i := 0; i < len(src8.Pix); i += 4 {
		src8.Pix[i+0] = uint8(i / 4)
		src8.Pix[i+1] = uint8(i / 4)
		src8.Pix[i+2] = uint8(i / 4)
		src8.Pix[i+3] = 0xff
	}
	src10 := makeTenBitGradient(32, 16)

	results := ResizeBatchWithDepth([]ResizeBatchItem{
		{Image: src8, Width: 16, Height: 8, Filter: Box},
		{Image: src10, Width: 16, Height: 8, Filter: Box},
	})
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].Err != nil || results[1].Err != nil {
		t.Fatalf("batch errors: %v, %v", results[0].Err, results[1].Err)
	}
	if _, ok := results[0].Image.(*image.NRGBA); !ok || results[0].BitDepth != BitDepth8 {
		t.Fatalf("first item type/depth = %T/%s, want *image.NRGBA/8",
			results[0].Image, results[0].BitDepth)
	}
	if _, ok := results[1].Image.(*image.NRGBA64); !ok || results[1].BitDepth != BitDepth10 {
		t.Fatalf("second item type/depth = %T/%s, want *image.NRGBA64/10",
			results[1].Image, results[1].BitDepth)
	}
}

func TestTenBitResizeDoesNotBandle(t *testing.T) {
	src := makeTenBitGradient(1024, 8)
	dst, err := ResizeWithDepth(src, 512, 8, Box)
	if err != nil {
		t.Fatalf("ResizeWithDepth: %v", err)
	}
	out := dst.(*image.NRGBA64)
	levels := make(map[uint16]struct{})
	maxDelta := uint16(0)
	for y := 0; y < out.Bounds().Dy(); y++ {
		var prev uint16
		for x := 0; x < out.Bounds().Dx(); x++ {
			off := y*out.Stride + x*8
			v := binary.BigEndian.Uint16(out.Pix[off : off+2])
			q := v >> 6
			levels[q] = struct{}{}
			if x > 0 && q > prev {
				if delta := q - prev; delta > maxDelta {
					maxDelta = delta
				}
			}
			prev = q
		}
	}
	if len(levels) <= 256 {
		t.Fatalf("resize retained %d levels; 8-bit collapse would cap at 256", len(levels))
	}
	if maxDelta > 2 {
		t.Fatalf("adjacent 10-bit step = %d, possible banding/aliasing", maxDelta)
	}
	t.Logf("banding probe retained %d levels, max adjacent step %d", len(levels), maxDelta)
}

func TestDeclaredDepthMismatch(t *testing.T) {
	img := image.NewNRGBA64(image.Rect(0, 0, 2, 1))
	p := img.Pix
	binary.BigEndian.PutUint16(p[0:2], 1023)
	binary.BigEndian.PutUint16(p[2:4], 0)
	binary.BigEndian.PutUint16(p[4:6], 0)
	binary.BigEndian.PutUint16(p[6:8], 0xffff)
	_, err := ConvertToDepth(img, WithDeclaredBitDepth(BitDepth8))
	if !errors.Is(err, ErrBitDepthMismatch) {
		t.Fatalf("error = %v, want ErrBitDepthMismatch", err)
	}
	if _, err := Resize10(img, 1, 1, Box, WithDeclaredBitDepth(BitDepth8)); !errors.Is(err, ErrBitDepthMismatch) {
		t.Fatalf("Resize10 error = %v, want ErrBitDepthMismatch", err)
	}
}

func TestBitDepthLogs(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	logger := func(level BitDepthLogLevel, format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, level.String()+": "+format)
	}

	src := makeTenBitGradient(16, 16)
	if _, err := ResizeWithDepth(src, 8, 8, Box, WithBitDepthLogger(logger)); err != nil {
		t.Fatalf("ResizeWithDepth: %v", err)
	}
	mu.Lock()
	got := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(got, "debug: imaging: bitdepth enter") ||
		!strings.Contains(got, "debug: imaging: bitdepth exit") {
		t.Fatalf("missing enter/exit logs:\n%s", got)
	}

	logs = nil
	_, err := ConvertToDepth(src, WithDeclaredBitDepth(BitDepth8), WithBitDepthLogger(logger))
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	mu.Lock()
	got = strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(got, "error: imaging: bitdepth fail") {
		t.Fatalf("missing failure log:\n%s", got)
	}
	if !strings.Contains(got, "debug: imaging: bitdepth enter") {
		t.Fatalf("missing enter log before failure:\n%s", got)
	}
}

func TestDefaultEightBitPathExact(t *testing.T) {
	img := mustOpen("testdata/branches.png")
	want := Resize(img, 123, 77, Lanczos)
	got, err := ResizeWithDepth(img, 123, 77, Lanczos)
	if err != nil {
		t.Fatalf("ResizeWithDepth: %v", err)
	}
	got8, ok := got.(*image.NRGBA)
	if !ok {
		t.Fatalf("output type = %T, want *image.NRGBA", got)
	}
	if !compareNRGBA(got8, want, 0) {
		t.Fatal("explicit 8-bit path changed the original Resize output")
	}
}

func TestDepthAwareStagesPreserveTenBit(t *testing.T) {
	src := makeTenBitGradient(16, 16)
	identity := [9]float64{
		0, 0, 0,
		0, 1, 0,
		0, 0, 0,
	}
	tests := []struct {
		name string
		fn   func() (image.Image, error)
	}{
		{"grayscale", func() (image.Image, error) { return GrayscaleWithDepth(src) }},
		{"invert", func() (image.Image, error) { return InvertWithDepth(src) }},
		{"contrast", func() (image.Image, error) { return AdjustContrastWithDepth(src, 10) }},
		{"blur", func() (image.Image, error) { return BlurWithDepth(src, 0.5) }},
		{"sharpen", func() (image.Image, error) { return SharpenWithDepth(src, 0.5) }},
		{"convolve", func() (image.Image, error) { return Convolve3x3WithDepth(src, identity, nil) }},
		{"flip", func() (image.Image, error) { return FlipHWithDepth(src) }},
		{"rotate", func() (image.Image, error) {
			return RotateWithDepth(src, 45, color.NRGBA64{A: 0xffff})
		}},
		{"crop", func() (image.Image, error) {
			return CropWithDepth(src, image.Rect(2, 2, 10, 10))
		}},
		{"paste", func() (image.Image, error) {
			return PasteWithDepth(src, src, image.Pt(2, 2))
		}},
		{"overlay", func() (image.Image, error) {
			return OverlayWithDepth(src, src, image.Pt(2, 2), 0.5)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if _, ok := dst.(*image.NRGBA64); !ok {
				t.Fatalf("%s type = %T, want *image.NRGBA64", tc.name, dst)
			}
			if depth := DetectBitDepth(dst); depth != BitDepth10 {
				t.Fatalf("%s depth = %s, want 10", tc.name, depth)
			}
		})
	}
}

func BenchmarkResizeDefault8(b *testing.B) {
	src := testdataBranchesPNG
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Resize(src, 150, 100, Lanczos)
	}
}

func BenchmarkResizeWithDepth8(b *testing.B) {
	src := testdataBranchesPNG
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ResizeWithDepth(src, 150, 100, Lanczos)
	}
}

func BenchmarkResizeTenBit(b *testing.B) {
	src := makeTenBitGradient(600, 400)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ResizeWithDepth(src, 150, 100, Lanczos)
	}
}

func makeTenBitGradient(width, height int) *image.NRGBA64 {
	img := image.NewNRGBA64(image.Rect(0, 0, width, height))
	total := width * height
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			q := uint16((x + y*width) * 1023 / maxInt(total-1, 1))
			off := y*img.Stride + x*8
			binary.BigEndian.PutUint16(img.Pix[off+0:off+2], expand10(q))
			binary.BigEndian.PutUint16(img.Pix[off+2:off+4], expand10(q))
			binary.BigEndian.PutUint16(img.Pix[off+4:off+6], expand10(q))
			binary.BigEndian.PutUint16(img.Pix[off+6:off+8], 0xffff)
		}
	}
	return img
}

func nrgba64Samples(img *image.NRGBA64) []uint16 {
	out := make([]uint16, 0, img.Bounds().Dx()*img.Bounds().Dy()*4)
	row := make([]uint16, img.Bounds().Dx()*4)
	src := newScanner64(img)
	for y := 0; y < img.Bounds().Dy(); y++ {
		src.scan(0, y, img.Bounds().Dx(), y+1, row)
		out = append(out, row...)
	}
	return out
}
