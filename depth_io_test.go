package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveOpenTenBitPNG(t *testing.T) {
	src := makeTenBitGradient(64, 32)
	dir := t.TempDir()
	name := filepath.Join(dir, "tenbit.png")
	if err := Save10(src, name); err != nil {
		t.Fatalf("Save10: %v", err)
	}

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 26 || string(data[1:4]) != "PNG" {
		t.Fatal("not a PNG file")
	}
	if data[24] != 16 {
		t.Fatalf("PNG bit depth byte = %d, want 16-bit container", data[24])
	}

	decoded, err := Open10(name)
	if err != nil {
		t.Fatalf("Open10: %v", err)
	}
	got, ok := decoded.(*image.NRGBA64)
	if !ok {
		t.Fatalf("decoded type = %T, want *image.NRGBA64", decoded)
	}
	if depth := DetectBitDepth(got); depth != BitDepth10 {
		t.Fatalf("decoded depth = %s, want 10", depth)
	}
	want := nrgba64Samples(src)
	gotSamples := nrgba64Samples(got)
	if len(want) != len(gotSamples) {
		t.Fatalf("sample count = %d, want %d", len(gotSamples), len(want))
	}
	for i := range want {
		if gotSamples[i] != want[i] {
			t.Fatalf("sample %d = 0x%04x, want 0x%04x", i, gotSamples[i], want[i])
		}
	}

	if _, err := Open(name, DecodeDeclaredBitDepth(BitDepth8)); !errors.Is(err, ErrBitDepthMismatch) {
		t.Fatalf("declared 8-bit decode error = %v, want ErrBitDepthMismatch", err)
	}
}

func TestTenBitUnsupportedContainerFailsWithoutPartialFile(t *testing.T) {
	src := makeTenBitGradient(32, 16)
	dir := t.TempDir()
	name := filepath.Join(dir, "tenbit.jpg")
	if err := os.WriteFile(name, []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}

	err := Save10(src, name)
	if !errors.Is(err, ErrUnsupportedBitDepth) {
		t.Fatalf("error = %v, want ErrUnsupportedBitDepth", err)
	}
	if !strings.Contains(err.Error(), "PNG") || !strings.Contains(err.Error(), "YUV420P10LE") {
		t.Fatalf("error does not suggest a 10-bit alternative: %v", err)
	}
	data, readErr := os.ReadFile(name)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "previous" {
		t.Fatalf("failed save replaced existing file with %q", data)
	}
	assertNoTempFiles(t, dir)

	var buf bytes.Buffer
	err = Encode10(&buf, src, JPEG)
	if !errors.Is(err, ErrUnsupportedBitDepth) {
		t.Fatalf("Encode10 JPEG error = %v, want ErrUnsupportedBitDepth", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("unsupported encode wrote %d bytes", buf.Len())
	}
}

func TestTenBitYUV420RoundTripAndSize(t *testing.T) {
	src := makeTenBitGradient(64, 32)
	dir := t.TempDir()
	name := filepath.Join(dir, "tenbit.yuv")
	if err := Save10(src, name); err != nil {
		t.Fatalf("Save10 yuv: %v", err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	expected := int64(src.Bounds().Dx() * src.Bounds().Dy() * 3)
	if info.Size() != expected {
		t.Fatalf("yuv420p10le size = %d, want %d", info.Size(), expected)
	}

	decoded, err := OpenYUV420P10LE(name, 64, 32)
	if err != nil {
		t.Fatalf("OpenYUV420P10LE: %v", err)
	}
	if decoded.Depth() != BitDepth10 {
		t.Fatalf("depth = %s, want 10", decoded.Depth())
	}
	for i, v := range decoded.Y {
		if v > 1023 {
			t.Fatalf("Y[%d] = %d exceeds 10-bit range", i, v)
		}
	}
	if len(decoded.U) != 32*16 || len(decoded.V) != 32*16 {
		t.Fatalf("chroma planes = %d/%d, want 32x16", len(decoded.U), len(decoded.V))
	}
	converted, err := ConvertToDepth(decoded, WithBitDepth(BitDepth10))
	if err != nil {
		t.Fatalf("ConvertToDepth yuv: %v", err)
	}
	if depth := DetectBitDepth(converted); depth != BitDepth10 {
		t.Fatalf("converted yuv depth = %s, want 10", depth)
	}
}

func TestEncodeDeclaredDepthMismatch(t *testing.T) {
	img := image.NewNRGBA64(image.Rect(0, 0, 1, 1))
	binary.BigEndian.PutUint16(img.Pix[0:2], 1023)
	binary.BigEndian.PutUint16(img.Pix[2:4], 0)
	binary.BigEndian.PutUint16(img.Pix[4:6], 0)
	binary.BigEndian.PutUint16(img.Pix[6:8], 0xffff)

	var buf bytes.Buffer
	err := Encode(&buf, img, PNG, EncodeDeclaredBitDepth(BitDepth8))
	if !errors.Is(err, ErrBitDepthMismatch) {
		t.Fatalf("error = %v, want ErrBitDepthMismatch", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("mismatched encode wrote %d bytes", buf.Len())
	}
}

func TestTenBitFileSizeReasonable(t *testing.T) {
	src := makeTenBitGradient(256, 128)
	dir := t.TempDir()
	png8 := filepath.Join(dir, "eight.png")
	png10 := filepath.Join(dir, "ten.png")
	if err := Save(src, png8, EncodeBitDepth(BitDepth8)); err != nil {
		t.Fatalf("Save 8-bit PNG: %v", err)
	}
	if err := Save10(src, png10); err != nil {
		t.Fatalf("Save 10-bit PNG: %v", err)
	}
	info8, err := os.Stat(png8)
	if err != nil {
		t.Fatal(err)
	}
	info10, err := os.Stat(png10)
	if err != nil {
		t.Fatal(err)
	}
	pngRatio := float64(info10.Size()) / float64(info8.Size())
	if pngRatio > 3.0 {
		t.Fatalf("10-bit PNG is %.2fx the 8-bit file (%d vs %d bytes)",
			pngRatio, info10.Size(), info8.Size())
	}

	yuv := filepath.Join(dir, "ten.yuv")
	if err := Save10(src, yuv); err != nil {
		t.Fatalf("Save 10-bit YUV: %v", err)
	}
	yuvInfo, err := os.Stat(yuv)
	if err != nil {
		t.Fatal(err)
	}
	yuv8Size := int64(src.Bounds().Dx() * src.Bounds().Dy() * 3 / 2)
	yuvRatio := float64(yuvInfo.Size()) / float64(yuv8Size)
	if yuvRatio > 2.1 {
		t.Fatalf("yuv420p10le is %.2fx the 8-bit yuv420p size", yuvRatio)
	}
	t.Logf("size ratio PNG10/PNG8=%.2fx YUV10/YUV8=%.2fx", pngRatio, yuvRatio)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
