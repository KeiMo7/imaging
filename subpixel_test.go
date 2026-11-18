package imaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"runtime"
	"testing"
	"time"
)

func TestCropSubpixelBlendsAdjacentPixels(t *testing.T) {
	src := &image.NRGBA{
		Pix: []uint8{
			0xff, 0x00, 0x00, 0xff,
			0x00, 0x00, 0xff, 0xff,
		},
		Stride: 8,
		Rect:   image.Rect(0, 0, 2, 1),
	}

	got, meta, err := CropSubpixel(src, FloatRect{MinX: 0.5, MinY: 0, MaxX: 1.5, MaxY: 1}, SubpixelCropOptions{
		Width:  1,
		Height: 1,
	})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	want := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	want.Pix = []uint8{0x80, 0x00, 0x80, 0xff}
	if !compareNRGBA(got, want, 0) {
		t.Fatalf("got %v, want %v", got.Pix, want.Pix)
	}
	if meta.Rounding != RoundHalfUp || meta.Filter != "linear" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestCropSubpixelOutputRounding(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	rect := FloatRect{MinX: 0, MinY: 0, MaxX: 2.5, MaxY: 2.5}

	got, meta, err := CropSubpixel(src, rect)
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if got.Bounds() != image.Rect(0, 0, 3, 3) {
		t.Fatalf("got bounds %v, want 3x3", got.Bounds())
	}
	if meta.Rounding != RoundHalfUp || meta.OutputWidth != 3 || meta.OutputHeight != 3 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	got, meta, err = CropSubpixel(src, rect, SubpixelCropOptions{Rounding: RoundDown})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if got.Bounds() != image.Rect(0, 0, 2, 2) || meta.Rounding != RoundDown {
		t.Fatalf("got bounds %v and metadata %+v, want 2x2 and RoundDown", got.Bounds(), meta)
	}

	got, _, err = CropSubpixel(src, FloatRect{MinX: 0, MinY: 0, MaxX: 2.1, MaxY: 2.1}, SubpixelCropOptions{Rounding: RoundUp})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if got.Bounds() != image.Rect(0, 0, 3, 3) {
		t.Fatalf("got bounds %v, want 3x3", got.Bounds())
	}

	got, _, err = CropSubpixel(src, rect, SubpixelCropOptions{Width: 0.49, Height: 2})
	if !errors.Is(err, ErrInvalidOutputSize) {
		t.Fatalf("got error %v, want ErrInvalidOutputSize", err)
	}
	if got != nil {
		t.Fatalf("got partial image %v on error", got)
	}
}

func TestCropSubpixelFillModes(t *testing.T) {
	src := &image.NRGBA{
		Pix: []uint8{
			10, 0, 0, 255,
			20, 0, 0, 255,
			30, 0, 0, 255,
		},
		Stride: 12,
		Rect:   image.Rect(0, 0, 3, 1),
	}
	fill := []uint8{200, 100, 50, 255}
	cases := []struct {
		name string
		mode FillMode
		want []uint8
	}{
		{
			name: "constant",
			mode: FillConstant,
			want: []uint8{
				200, 100, 50, 255,
				200, 100, 50, 255,
				10, 0, 0, 255,
			},
		},
		{
			name: "edge",
			mode: FillEdge,
			want: []uint8{
				10, 0, 0, 255,
				10, 0, 0, 255,
				10, 0, 0, 255,
			},
		},
		{
			name: "mirror",
			mode: FillMirror,
			want: []uint8{
				20, 0, 0, 255,
				10, 0, 0, 255,
				10, 0, 0, 255,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, meta, err := CropSubpixel(src, FloatRect{MinX: -2, MinY: 0, MaxX: 1, MaxY: 1}, SubpixelCropOptions{
				Width:     3,
				Height:    1,
				FillColor: fill,
				Fill:      EdgeFillConfig{Left: tc.mode},
			})
			if err != nil {
				t.Fatalf("CropSubpixel returned an error: %v", err)
			}
			if !bytes.Equal(got.Pix, tc.want) {
				t.Fatalf("got %v, want %v", got.Pix, tc.want)
			}
			if meta.Fill.Left != tc.mode {
				t.Fatalf("metadata fill mode = %v, want %v", meta.Fill.Left, tc.mode)
			}
		})
	}
}

func TestCropSubpixelPerEdgeFill(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			c := uint8(20 + x*20 + y*10)
			src.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}
	fill := []uint8{200, 20, 20, 255}

	topEdge, _, err := CropSubpixel(src, FloatRect{MinX: 0, MinY: -1, MaxX: 4, MaxY: 1}, SubpixelCropOptions{
		Width:     4,
		Height:    2,
		FillColor: fill,
		Fill:      EdgeFillConfig{Top: FillEdge},
	})
	if err != nil {
		t.Fatalf("top edge crop failed: %v", err)
	}
	topConstant, _, err := CropSubpixel(src, FloatRect{MinX: 0, MinY: -1, MaxX: 4, MaxY: 1}, SubpixelCropOptions{
		Width:     4,
		Height:    2,
		FillColor: fill,
		Fill:      EdgeFillConfig{Top: FillConstant},
	})
	if err != nil {
		t.Fatalf("top constant crop failed: %v", err)
	}
	if bytes.Equal(topEdge.Pix[:topEdge.Stride], topConstant.Pix[:topConstant.Stride]) {
		t.Fatal("changing only the top fill mode did not change the top output row")
	}
	if !bytes.Equal(topEdge.Pix[topEdge.Stride:], topConstant.Pix[topConstant.Stride:]) {
		t.Fatal("changing only the top fill mode changed an in-bounds output row")
	}

	rightEdge, _, err := CropSubpixel(src, FloatRect{MinX: 2, MinY: 0, MaxX: 5, MaxY: 4}, SubpixelCropOptions{
		Width:     3,
		Height:    4,
		FillColor: fill,
		Fill:      EdgeFillConfig{Right: FillEdge},
	})
	if err != nil {
		t.Fatalf("right edge crop failed: %v", err)
	}
	rightConstant, _, err := CropSubpixel(src, FloatRect{MinX: 2, MinY: 0, MaxX: 5, MaxY: 4}, SubpixelCropOptions{
		Width:     3,
		Height:    4,
		FillColor: fill,
		Fill:      EdgeFillConfig{Right: FillConstant},
	})
	if err != nil {
		t.Fatalf("right constant crop failed: %v", err)
	}
	leftColumnsEqual := true
	rightColumnsDiffer := false
	for y := 0; y < 4; y++ {
		left1 := rightEdge.Pix[y*rightEdge.Stride : y*rightEdge.Stride+4]
		left2 := rightConstant.Pix[y*rightConstant.Stride : y*rightConstant.Stride+4]
		last1 := rightEdge.Pix[y*rightEdge.Stride+8 : y*rightEdge.Stride+12]
		last2 := rightConstant.Pix[y*rightConstant.Stride+8 : y*rightConstant.Stride+12]
		if !bytes.Equal(left1, left2) {
			leftColumnsEqual = false
		}
		if !bytes.Equal(last1, last2) {
			rightColumnsDiffer = true
		}
	}
	if !leftColumnsEqual {
		t.Fatal("changing only the right fill mode changed an in-bounds column")
	}
	if !rightColumnsDiffer {
		t.Fatal("changing only the right fill mode did not change the right output column")
	}
}

func TestCropSubpixelAlphaAndGrayscaleFill(t *testing.T) {
	rgbaSrc := &image.NRGBA{
		Pix: []uint8{
			10, 20, 30, 40,
			50, 60, 70, 80,
		},
		Stride: 8,
		Rect:   image.Rect(0, 0, 2, 1),
	}
	got, meta, err := CropSubpixel(rgbaSrc, FloatRect{MinX: 1, MinY: 0, MaxX: 3, MaxY: 1}, SubpixelCropOptions{
		Width:     2,
		Height:    1,
		FillColor: []uint8{200, 100, 50, 128},
		Fill:      EdgeFillConfig{Right: FillConstant},
	})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if got.Pix[4] != 200 || got.Pix[5] != 100 || got.Pix[6] != 50 || got.Pix[7] != 128 {
		t.Fatalf("got fill pixel %v, want [200 100 50 128]", got.Pix[4:8])
	}
	if meta.Channels != 4 {
		t.Fatalf("got channels %d, want 4", meta.Channels)
	}

	graySrc := &image.Gray{
		Pix:    []uint8{10, 20, 40, 50},
		Stride: 2,
		Rect:   image.Rect(0, 0, 2, 2),
	}
	got, meta, err = CropSubpixel(graySrc, FloatRect{MinX: 1, MinY: 0, MaxX: 3, MaxY: 2}, SubpixelCropOptions{
		Width:     2,
		Height:    2,
		FillColor: []uint8{200},
		Fill:      EdgeFillConfig{Right: FillConstant},
	})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error for grayscale input: %v", err)
	}
	if !bytes.Equal(got.Pix[4:8], []uint8{200, 200, 200, 255}) {
		t.Fatalf("got grayscale fill pixel %v, want [200 200 200 255]", got.Pix[4:8])
	}
	if meta.Channels != 1 {
		t.Fatalf("got channels %d, want 1", meta.Channels)
	}
}

func TestCropSubpixelChannelMismatch(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	got, _, err := CropSubpixel(src, FloatRect{MinX: 1, MinY: 0, MaxX: 3, MaxY: 2}, SubpixelCropOptions{
		FillColor: []uint8{1, 2, 3},
	})
	if !errors.Is(err, ErrFillColorChannels) {
		t.Fatalf("got error %v, want ErrFillColorChannels", err)
	}
	var channelErr *FillColorChannelError
	if !errors.As(err, &channelErr) {
		t.Fatalf("got error type %T, want *FillColorChannelError", err)
	}
	if channelErr.Expected != 4 {
		t.Fatalf("got expected channels %d, want 4", channelErr.Expected)
	}
	if got != nil {
		t.Fatalf("got partial image %v on error", got)
	}

	graySrc := image.NewGray(image.Rect(0, 0, 2, 2))
	_, _, err = CropSubpixel(graySrc, FloatRect{MinX: 1, MinY: 0, MaxX: 3, MaxY: 2}, SubpixelCropOptions{
		FillColor: []uint8{1, 2, 3, 4},
	})
	if !errors.Is(err, ErrFillColorChannels) {
		t.Fatalf("got error %v, want ErrFillColorChannels", err)
	}
	if !errors.As(err, &channelErr) || channelErr.Expected != 1 {
		t.Fatalf("got error %v, want expected channel count 1", err)
	}
}

func TestCropSubpixelCompletelyOutside(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for _, rect := range []FloatRect{
		{MinX: 4, MinY: 0, MaxX: 8, MaxY: 4},
		{MinX: -8, MinY: -8, MaxX: -4, MaxY: -4},
		{MinX: 2, MinY: 8, MaxX: 6, MaxY: 10},
	} {
		got, _, err := CropSubpixel(src, rect)
		if !errors.Is(err, ErrCropOutside) {
			t.Fatalf("rect %+v: got error %v, want ErrCropOutside", rect, err)
		}
		if got != nil {
			t.Fatalf("rect %+v: got partial image %v on error", rect, got)
		}
	}
}

func TestCropSubpixelIsDeterministicAcrossProcCounts(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			src.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x*3 + y),
				G: uint8(x + y*5),
				B: uint8(x*7 + y*11),
				A: uint8(64 + (x*13+y*17)%192),
			})
		}
	}

	opts := SubpixelCropOptions{
		Width:     53,
		Height:    41,
		Rounding:  RoundHalfUp,
		FillColor: []uint8{12, 34, 56, 78},
		Fill: EdgeFillConfig{
			Top:    FillMirror,
			Right:  FillEdge,
			Bottom: FillConstant,
			Left:   FillMirror,
		},
	}
	rect := FloatRect{MinX: -3.35, MinY: -2.7, MaxX: 60.8, MaxY: 45.15}

	previousProcs := runtime.GOMAXPROCS(1)
	first, _, err := CropSubpixel(src, rect, opts)
	if err != nil {
		runtime.GOMAXPROCS(previousProcs)
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}

	runtime.GOMAXPROCS(8)
	second, _, err := CropSubpixel(src, rect, opts)
	runtime.GOMAXPROCS(previousProcs)
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}

	if !bytes.Equal(first.Pix, second.Pix) {
		t.Fatal("subpixel crop output changed with parallel proc count")
	}
}

func TestCropSubpixelMatchesLegacyCropForIntegerBounds(t *testing.T) {
	src := testdataBranchesPNG
	rect := image.Rect(100, 80, 300, 260)
	want := Crop(src, rect)
	got, _, err := CropSubpixel(src, FloatRectFrom(rect))
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if !compareNRGBA(got, want, 0) {
		t.Fatal("integer CropSubpixel output differs from legacy Crop")
	}
}

func TestCropSubpixelUsesImageCoordinateSpace(t *testing.T) {
	src := &image.NRGBA{
		Pix: []uint8{
			0x00, 0x00, 0xff, 0xff,
			0xff, 0x00, 0x00, 0xff,
		},
		Stride: 8,
		Rect:   image.Rect(10, 20, 12, 21),
	}
	got, meta, err := CropSubpixel(src, FloatRect{MinX: 10.5, MinY: 20, MaxX: 11.5, MaxY: 21}, SubpixelCropOptions{
		Width:  1,
		Height: 1,
	})
	if err != nil {
		t.Fatalf("CropSubpixel returned an error: %v", err)
	}
	if !bytes.Equal(got.Pix, []uint8{0x80, 0x00, 0x80, 0xff}) {
		t.Fatalf("got %v, want [128 0 128 255]", got.Pix)
	}
	if meta.SourceBounds != src.Bounds() {
		t.Fatalf("metadata source bounds = %v, want %v", meta.SourceBounds, src.Bounds())
	}
}

type cropReplayFixture struct {
	Width   int                 `json:"width"`
	Height  int                 `json:"height"`
	Pix     []uint8             `json:"pix"`
	Rect    FloatRect           `json:"rect"`
	Options SubpixelCropOptions `json:"options"`
}

func TestCropSubpixelSerializedReplay(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 9, 7))
	for i := range src.Pix {
		src.Pix[i] = uint8((i * 37) % 256)
	}
	fixture := cropReplayFixture{
		Width:  9,
		Height: 7,
		Pix:    append([]uint8(nil), src.Pix...),
		Rect:   FloatRect{MinX: -1.25, MinY: 0.4, MaxX: 7.6, MaxY: 6.9},
		Options: SubpixelCropOptions{
			Width:      8,
			Height:     7,
			Rounding:   RoundUp,
			FilterName: "linear",
			FillColor:  []uint8{11, 22, 33, 44},
			Fill: EdgeFillConfig{
				Top:    FillMirror,
				Right:  FillConstant,
				Bottom: FillEdge,
				Left:   FillMirror,
			},
		},
	}

	first, firstMeta, err := CropSubpixel(src, fixture.Rect, fixture.Options)
	if err != nil {
		t.Fatalf("initial CropSubpixel failed: %v", err)
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("failed to serialize fixture: %v", err)
	}

	var replayed cropReplayFixture
	if err := json.Unmarshal(encoded, &replayed); err != nil {
		t.Fatalf("failed to deserialize fixture: %v", err)
	}
	replayedSrc := &image.NRGBA{
		Pix:    replayed.Pix,
		Stride: replayed.Width * 4,
		Rect:   image.Rect(0, 0, replayed.Width, replayed.Height),
	}
	second, secondMeta, err := CropSubpixel(replayedSrc, replayed.Rect, replayed.Options)
	if err != nil {
		t.Fatalf("replayed CropSubpixel failed: %v", err)
	}

	if !bytes.Equal(first.Pix, second.Pix) {
		t.Fatal("serialized replay produced different pixels")
	}
	if firstMeta.Rounding != secondMeta.Rounding || firstMeta.Filter != secondMeta.Filter ||
		firstMeta.Fill != secondMeta.Fill || !bytes.Equal(firstMeta.FillColor, secondMeta.FillColor) {
		t.Fatalf("serialized replay metadata differs: %+v != %+v", firstMeta, secondMeta)
	}
}

func TestCropSubpixelComparisonReport(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 128, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			src.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*5 + y*3) % 256),
				G: uint8((x*2 + y*7) % 256),
				B: uint8((x*11 + y) % 256),
				A: 255,
			})
		}
	}

	integerRect := image.Rect(17, 13, 101, 77)
	subpixelRect := FloatRect{MinX: 17.35, MinY: 13.6, MaxX: 100.7, MaxY: 76.9}
	opts := SubpixelCropOptions{Width: 84, Height: 64}

	const iterations = 100
	start := time.Now()
	var integer *image.NRGBA
	for i := 0; i < iterations; i++ {
		integer = Crop(src, integerRect)
	}
	integerDuration := time.Since(start) / iterations

	start = time.Now()
	var subpixel *image.NRGBA
	for i := 0; i < iterations; i++ {
		var err error
		subpixel, _, err = CropSubpixel(src, subpixelRect, opts)
		if err != nil {
			t.Fatalf("CropSubpixel returned an error: %v", err)
		}
	}
	subpixelDuration := time.Since(start) / iterations

	differingPixels, maxChannelDelta, sumAbsDelta := imageDifference(integer, subpixel)
	if differingPixels == 0 {
		t.Fatal("expected integer and subpixel crops to differ")
	}
	t.Logf(
		"integer crop: %v, subpixel crop: %v, differing pixels: %d/%d, max channel delta: %d, sum abs delta: %d",
		integerDuration,
		subpixelDuration,
		differingPixels,
		integer.Bounds().Dx()*integer.Bounds().Dy(),
		maxChannelDelta,
		sumAbsDelta,
	)
}

func BenchmarkCropSubpixel(b *testing.B) {
	src := image.NewNRGBA(image.Rect(0, 0, 128, 96))
	rect := FloatRect{MinX: 17.35, MinY: 13.6, MaxX: 100.7, MaxY: 76.9}
	opts := SubpixelCropOptions{
		Width:     84,
		Height:    64,
		FillColor: []uint8{0, 0, 0, 0},
		Fill: EdgeFillConfig{
			Top:    FillMirror,
			Right:  FillEdge,
			Bottom: FillConstant,
			Left:   FillMirror,
		},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = CropSubpixel(src, rect, opts)
	}
}

func imageDifference(a, b *image.NRGBA) (differingPixels, maxChannelDelta, sumAbsDelta int) {
	if !a.Bounds().Eq(b.Bounds()) {
		return -1, -1, -1
	}
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			i := y*a.Stride + x*4
			pixelDiffers := false
			for c := 0; c < 4; c++ {
				delta := absint(int(a.Pix[i+c]) - int(b.Pix[i+c]))
				if delta != 0 {
					pixelDiffers = true
				}
				if delta > maxChannelDelta {
					maxChannelDelta = delta
				}
				sumAbsDelta += delta
			}
			if pixelDiffers {
				differingPixels++
			}
		}
	}
	return differingPixels, maxChannelDelta, sumAbsDelta
}
