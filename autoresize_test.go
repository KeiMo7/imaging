package imaging

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecideResizeDownscaleTiers(t *testing.T) {
	policy := DefaultAutoResizePolicy()
	tests := []struct {
		name       string
		target     int
		wantFilter string
		wantTier   DownscaleTier
	}{
		{name: "heavy", target: 200, wantFilter: "Lanczos", wantTier: DownscaleTierHeavy},
		{name: "medium", target: 500, wantFilter: "CatmullRom", wantTier: DownscaleTierMedium},
		{name: "light", target: 900, wantFilter: "Linear", wantTier: DownscaleTierLight},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := DecideResize(ResizeDecisionInput{
				SrcWidth:  1000,
				SrcHeight: 1000,
				DstWidth:  tt.target,
				DstHeight: tt.target,
				Metadata:  InputMetadata{Kind: ImageKindPhoto},
			}, policy)
			if err != nil {
				t.Fatalf("DecideResize returned error: %v", err)
			}
			if decision.FilterName != tt.wantFilter {
				t.Fatalf("filter = %q, want %q", decision.FilterName, tt.wantFilter)
			}
			if decision.Tier != tt.wantTier {
				t.Fatalf("tier = %q, want %q", decision.Tier, tt.wantTier)
			}
			if decision.Mode != ScaleModeDownscale {
				t.Fatalf("mode = %q, want downscale", decision.Mode)
			}
			if len(decision.Reasons) == 0 {
				t.Fatal("decision has no reasons")
			}
		})
	}
}

func TestDecideResizeUpscaleKindsAndHintPriority(t *testing.T) {
	policy := DefaultAutoResizePolicy()
	tests := []struct {
		name       string
		hint       SceneHint
		kind       ImageKind
		wantFilter string
	}{
		{name: "photo", kind: ImageKindPhoto, wantFilter: "MitchellNetravali"},
		{name: "graphic", kind: ImageKindGraphic, wantFilter: "NearestNeighbor"},
		{name: "unknown conservative", kind: ImageKindUnknown, wantFilter: "Linear"},
		{name: "portrait hint overrides graphic", hint: SceneHintPortrait, kind: ImageKindGraphic, wantFilter: "MitchellNetravali"},
		{name: "screenshot hint overrides photo", hint: SceneHintScreenshot, kind: ImageKindPhoto, wantFilter: "NearestNeighbor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := DecideResize(ResizeDecisionInput{
				SrcWidth:  100,
				SrcHeight: 100,
				DstWidth:  200,
				DstHeight: 200,
				Hint:      tt.hint,
				Metadata:  InputMetadata{Kind: tt.kind},
			}, policy)
			if err != nil {
				t.Fatalf("DecideResize returned error: %v", err)
			}
			if decision.FilterName != tt.wantFilter {
				t.Fatalf("filter = %q, want %q", decision.FilterName, tt.wantFilter)
			}
		})
	}
}

func TestDecideResizeGraphicDownscaleAvoidsCubic(t *testing.T) {
	decision, err := DecideResize(ResizeDecisionInput{
		SrcWidth:  1000,
		SrcHeight: 1000,
		DstWidth:  500,
		DstHeight: 500,
		Hint:      SceneHintScreenshot,
	}, DefaultAutoResizePolicy())
	if err != nil {
		t.Fatalf("DecideResize returned error: %v", err)
	}
	if decision.FilterName != "Box" {
		t.Fatalf("screenshot medium downscale filter = %q, want Box", decision.FilterName)
	}
}

func TestDecideResizeNonUniformNearSquareAndChannels(t *testing.T) {
	decision, err := DecideResize(ResizeDecisionInput{
		SrcWidth:  1000,
		SrcHeight: 980,
		DstWidth:  500,
		DstHeight: 980,
		Metadata: InputMetadata{
			Kind:        ImageKindPhoto,
			Channels:    4,
			HasAlpha:    true,
			HasAlphaSet: true,
		},
	}, DefaultAutoResizePolicy())
	if err != nil {
		t.Fatalf("DecideResize returned error: %v", err)
	}
	if decision.Mode != ScaleModeDownscale {
		t.Fatalf("mode = %q, want downscale", decision.Mode)
	}
	if decision.Uniform {
		t.Fatal("one-axis resize was classified as uniform")
	}
	if !decision.NearlySquare {
		t.Fatalf("source aspect %.4f was not classified as nearly square", decision.SourceAspect)
	}
	if !decision.HasAlpha || decision.Channels != 4 {
		t.Fatalf("channel evidence = channels:%d alpha:%t", decision.Channels, decision.HasAlpha)
	}
	if !containsReason(decision.Reasons, "aspect=non_uniform") {
		t.Fatalf("missing non-uniform reason: %v", decision.Reasons)
	}

	gray, err := DecideResize(ResizeDecisionInput{
		SrcWidth:  80,
		SrcHeight: 60,
		DstWidth:  40,
		DstHeight: 30,
		Metadata: InputMetadata{
			Channels:     1,
			Grayscale:    true,
			GrayscaleSet: true,
		},
	}, DefaultAutoResizePolicy())
	if err != nil {
		t.Fatalf("DecideResize gray returned error: %v", err)
	}
	if gray.Channels != 1 || !gray.Grayscale || gray.HasAlpha {
		t.Fatalf("gray evidence = channels:%d grayscale:%t alpha:%t", gray.Channels, gray.Grayscale, gray.HasAlpha)
	}
}

func TestDecideResizeMixedAxisFallsBack(t *testing.T) {
	decision, err := DecideResize(ResizeDecisionInput{
		SrcWidth:  100,
		SrcHeight: 100,
		DstWidth:  200,
		DstHeight: 50,
		Metadata:  InputMetadata{Kind: ImageKindPhoto},
	}, DefaultAutoResizePolicy())
	if err != nil {
		t.Fatalf("DecideResize returned error: %v", err)
	}
	if decision.Mode != ScaleModeMixed {
		t.Fatalf("mode = %q, want mixed", decision.Mode)
	}
	if decision.FilterName != "Linear" {
		t.Fatalf("mixed-axis filter = %q, want Linear", decision.FilterName)
	}
}

func TestResizeAutoWithGrayAndAlpha(t *testing.T) {
	options := DefaultAutoResizeOptions()
	options.Metadata.Kind = ImageKindPhoto

	gray := image.NewGray(image.Rect(0, 0, 80, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			gray.SetGray(x, y, color.Gray{Y: uint8((x * 7) % 256)})
		}
	}
	out, report, err := ResizeAuto(gray, 40, 30, options)
	if err != nil {
		t.Fatalf("ResizeAuto gray returned error: %v", err)
	}
	assertOutputSize(t, out, 40, 30)
	if report.Input.Metadata.Channels != 1 || !report.Input.Metadata.Grayscale {
		t.Fatalf("gray metadata = %+v", report.Input.Metadata)
	}
	if report.Output == nil || report.Output.Duration <= 0 {
		t.Fatalf("missing output timing: %+v", report.Output)
	}

	alpha := image.NewNRGBA(image.Rect(0, 0, 80, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			alpha.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 3),
				G: uint8(y * 4),
				B: 120,
				A: uint8(20 + (x+y)%236),
			})
		}
	}
	out, report, err = ResizeAuto(alpha, 40, 30, options)
	if err != nil {
		t.Fatalf("ResizeAuto alpha returned error: %v", err)
	}
	assertOutputSize(t, out, 40, 30)
	if report.Input.Metadata.Channels != 4 || !report.Input.Metadata.HasAlpha {
		t.Fatalf("alpha metadata = %+v", report.Input.Metadata)
	}
	if !hasNonOpaquePixel(out) {
		t.Fatal("alpha resize produced an unexpectedly opaque image")
	}
}

func TestResizeAutoValidationAndDisabledDefault(t *testing.T) {
	valid := image.NewNRGBA(image.Rect(0, 0, 10, 10))

	out, report, err := ResizeAuto(valid, 5, 5, AutoResizeOptions{})
	if !errors.Is(err, ErrAutoResizeDisabled) {
		t.Fatalf("disabled policy error = %v, want ErrAutoResizeDisabled", err)
	}
	if out != nil {
		t.Fatal("disabled policy returned a partial image")
	}
	if report == nil || report.Status != "error" {
		t.Fatalf("disabled policy report = %+v", report)
	}

	out, report, err = ResizeAuto(nil, 5, 5, DefaultAutoResizeOptions())
	if !errors.Is(err, ErrInvalidImageSize) {
		t.Fatalf("nil image error = %v, want ErrInvalidImageSize", err)
	}
	if out != nil || report.Error == "" {
		t.Fatalf("nil image result out=%v report=%+v", out, report)
	}

	zero := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	out, _, err = ResizeAuto(zero, 5, 5, DefaultAutoResizeOptions())
	if !errors.Is(err, ErrInvalidImageSize) || out != nil {
		t.Fatalf("zero source result out=%v err=%v", out, err)
	}

	out, _, err = ResizeAuto(valid, 0, 5, DefaultAutoResizeOptions())
	if !errors.Is(err, ErrInvalidResizeSize) || out != nil {
		t.Fatalf("zero target result out=%v err=%v", out, err)
	}
}

func TestResizeAutoCumulativeScaleWarningAndRejection(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	options := DefaultAutoResizeOptions()
	options.Metadata.Kind = ImageKindPhoto
	options.History = AppendResizeStep(ResizeHistory{}, 400, 400, 100, 100, "Lanczos")

	out, report, err := ResizeAuto(src, 40, 40, options)
	if !errors.Is(err, ErrCumulativeRescale) {
		t.Fatalf("cumulative error = %v, want ErrCumulativeRescale", err)
	}
	if out != nil {
		t.Fatal("cumulative rejection returned a partial image")
	}
	if report == nil || !report.Decision.CumulativeExceeded || report.Warning == "" {
		t.Fatalf("missing cumulative warning in report: %+v", report)
	}
	if !strings.Contains(report.Warning, "original image") {
		t.Fatalf("warning does not recommend one-shot resize: %q", report.Warning)
	}

	options.Policy.AllowCumulativeDownscale = true
	out, report, err = ResizeAuto(src, 40, 40, options)
	if err != nil {
		t.Fatalf("explicit cumulative override returned error: %v", err)
	}
	if out == nil || report.Warning == "" {
		t.Fatalf("override did not run with warning: out=%v report=%+v", out, report)
	}
}

func TestResizeBatchSkipsBadJobsAndCanRetry(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.png")
	if err := Save(image.NewNRGBA(image.Rect(0, 0, 12, 8)), validPath); err != nil {
		t.Fatalf("Save valid image: %v", err)
	}
	truncatedPath := filepath.Join(dir, "truncated.png")
	if err := os.WriteFile(truncatedPath, []byte("\x89PNG\r\n\x1a\n\x00\x00"), 0o600); err != nil {
		t.Fatalf("write truncated image: %v", err)
	}

	validImage := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	zeroImage := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	jobs := []ResizeBatchJob{
		{ID: "truncated", Path: truncatedPath, Width: 6, Height: 4},
		{ID: "zero", Image: zeroImage, Width: 6, Height: 4},
		{ID: "valid-image", Image: validImage, Width: 10, Height: 10},
		{ID: "valid-file", Path: validPath, Width: 6, Height: 4},
	}
	result := ResizeBatch(jobs, DefaultAutoResizePolicy())
	if result.Summary.Total != 4 || result.Summary.Succeeded != 2 || result.Summary.Skipped != 2 {
		t.Fatalf("batch summary = %+v", result.Summary)
	}
	if result.Items[2].Image == nil || result.Items[3].Image == nil {
		t.Fatal("valid jobs did not produce images after earlier failures")
	}
	if !strings.Contains(result.Items[0].SkipReason, "decode_failed") {
		t.Fatalf("truncated skip reason = %q", result.Items[0].SkipReason)
	}
	if !errors.Is(errorFromSkipped(result.Items[1]), ErrInvalidImageSize) {
		t.Fatalf("zero-size skip report = %+v", result.Items[1].Report)
	}
	if len(result.SkippedJobs()) != 2 {
		t.Fatalf("SkippedJobs length = %d, want 2", len(result.SkippedJobs()))
	}

	retry := result.RetrySkipped(map[string]ResizeBatchJob{
		"truncated": {ID: "truncated", Path: validPath, Width: 6, Height: 4},
		"zero":      {ID: "zero", Image: validImage, Width: 6, Height: 4},
	}, DefaultAutoResizePolicy())
	if retry.Summary.Succeeded != 2 || retry.Summary.Skipped != 0 {
		t.Fatalf("retry summary = %+v", retry.Summary)
	}
}

func TestInspectImageMetadataDoesNotReadPixels(t *testing.T) {
	src := noPixelReadImage{
		rect:  image.Rect(0, 0, 20, 10),
		model: color.NRGBAModel,
	}
	meta := inspectImageMetadata(src)
	if meta.Channels != 4 || !meta.HasAlpha {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestAutoResizeAcceptanceComparison(t *testing.T) {
	src, err := Open("testdata/flowers.png")
	if err != nil {
		t.Fatalf("Open flowers.png: %v", err)
	}
	options := DefaultAutoResizeOptions()
	options.Metadata.Kind = ImageKindPhoto

	specs := []struct {
		name       string
		width      int
		height     int
		wantFilter string
	}{
		{name: "thumbnail-heavy", width: 120, height: 80, wantFilter: "Lanczos"},
		{name: "list-medium", width: 300, height: 200, wantFilter: "CatmullRom"},
		{name: "detail-light", width: 540, height: 360, wantFilter: "Linear"},
	}
	for _, spec := range specs {
		started := time.Now()
		out, report, err := ResizeAuto(src, spec.width, spec.height, options)
		if err != nil {
			t.Fatalf("%s: ResizeAuto: %v", spec.name, err)
		}
		assertOutputSize(t, out, spec.width, spec.height)
		if report.Decision.FilterName != spec.wantFilter {
			t.Fatalf("%s: filter = %q, want %q", spec.name, report.Decision.FilterName, spec.wantFilter)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("%s: marshal report: %v", spec.name, err)
		}
		t.Logf(
			"%s src=%dx%d dst=%dx%d filter=%s elapsed=%s report=%s",
			spec.name,
			report.Input.Width,
			report.Input.Height,
			report.Output.Width,
			report.Output.Height,
			report.Decision.FilterName,
			time.Since(started),
			encoded,
		)
	}
}

func TestAutoResizeBatchReplayAgreement(t *testing.T) {
	type replayCase struct {
		job      ResizeBatchJob
		expected string
	}
	cases := []replayCase{
		{
			job: ResizeBatchJob{
				ID: "branches-photo-heavy", Path: "testdata/branches.jpg",
				Width: 120, Height: 80,
				Metadata: InputMetadata{Kind: ImageKindPhoto},
			},
			expected: "Lanczos",
		},
		{
			job: ResizeBatchJob{
				ID: "branches-photo-medium", Path: "testdata/branches.png",
				Width: 300, Height: 200,
				Metadata: InputMetadata{Kind: ImageKindPhoto},
			},
			expected: "CatmullRom",
		},
		{
			job: ResizeBatchJob{
				ID: "flowers-screenshot", Path: "testdata/flowers.png",
				Width: 300, Height: 200,
				Hint: SceneHintScreenshot,
			},
			expected: "Box",
		},
		{
			job: ResizeBatchJob{
				ID: "flowers-icon-light", Path: "testdata/flowers_small.png",
				Width: 220, Height: 140,
				Hint: SceneHintIcon,
			},
			expected: "NearestNeighbor",
		},
		{
			job: ResizeBatchJob{
				ID: "unknown-conservative", Path: "testdata/orientation_1.jpg",
				Width: 30, Height: 42,
			},
			expected: "Linear",
		},
	}

	jobs := make([]ResizeBatchJob, len(cases))
	for i := range cases {
		jobs[i] = cases[i].job
	}
	result := ResizeBatch(jobs, DefaultAutoResizePolicy())
	if result.Summary.Succeeded != len(cases) {
		t.Fatalf("batch replay summary = %+v", result.Summary)
	}

	matches := 0
	for i, item := range result.Items {
		got := item.Report.Decision.FilterName
		if got == cases[i].expected {
			matches++
		}
		reasons, err := json.Marshal(item.Report.Decision.Reasons)
		if err != nil {
			t.Fatalf("%s: marshal reasons: %v", item.ID, err)
		}
		t.Logf(
			"sample=%s expected=%s selected=%s match=%t mode=%s kind=%s reasons=%s",
			item.ID,
			cases[i].expected,
			got,
			got == cases[i].expected,
			item.Report.Decision.Mode,
			item.Report.Decision.ImageKind,
			reasons,
		)
	}
	agreement := float64(matches) / float64(len(cases))
	t.Logf("batch replay agreement=%d/%d (%.1f%%)", matches, len(cases), agreement*100)
	if agreement != 1 {
		t.Fatalf("manual agreement = %.1f%%, want 100%%", agreement*100)
	}
}

func assertOutputSize(t *testing.T, img *image.NRGBA, width, height int) {
	t.Helper()
	if img == nil {
		t.Fatal("output image is nil")
	}
	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Fatalf("output size = %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), width, height)
	}
}

func containsReason(reasons []string, substring string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, substring) {
			return true
		}
	}
	return false
}

func hasNonOpaquePixel(img *image.NRGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return true
		}
	}
	return false
}

func errorFromSkipped(item ResizeBatchItem) error {
	if item.Report == nil || item.Report.Error == "" {
		return nil
	}
	if strings.Contains(item.Report.Error, ErrInvalidImageSize.Error()) {
		return ErrInvalidImageSize
	}
	return errors.New(item.Report.Error)
}

type noPixelReadImage struct {
	rect  image.Rectangle
	model color.Model
}

func (img noPixelReadImage) ColorModel() color.Model { return img.model }
func (img noPixelReadImage) Bounds() image.Rectangle { return img.rect }
func (img noPixelReadImage) At(int, int) color.Color {
	panic("metadata inspection must not read pixels")
}
