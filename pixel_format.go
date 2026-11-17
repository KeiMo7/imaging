package imaging

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

// YUV420P10LE is a planar, full-range BT.709 YCbCr image with 10 significant
// bits per sample. Samples are stored right-aligned in the range 0..1023.
type YUV420P10LE struct {
	Width  int
	Height int
	Y      []uint16
	U      []uint16
	V      []uint16
}

// Depth implements DepthProvider.
func (img *YUV420P10LE) Depth() BitDepth {
	return BitDepth10
}

// ColorModel implements image.Image.
func (img *YUV420P10LE) ColorModel() color.Model {
	return color.NRGBA64Model
}

// Bounds implements image.Image.
func (img *YUV420P10LE) Bounds() image.Rectangle {
	if img == nil || img.Width <= 0 || img.Height <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(0, 0, img.Width, img.Height)
}

// At implements image.Image.
func (img *YUV420P10LE) At(x, y int) color.Color {
	if img == nil || !image.Pt(x, y).In(img.Bounds()) {
		return color.NRGBA64{}
	}
	w := img.Width
	off := y*w + x
	cw := (w + 1) / 2
	ci := (y/2)*cw + x/2
	r, g, b := yuv10ToRGB(img.Y[off], img.U[ci], img.V[ci])
	return color.NRGBA64{
		R: expand10(r),
		G: expand10(g),
		B: expand10(b),
		A: 0xffff,
	}
}

func (img *YUV420P10LE) scanRGB(x1, y1, x2, y2 int, dst []uint16) {
	cw := (img.Width + 1) / 2
	j := 0
	for y := y1; y < y2; y++ {
		for x := x1; x < x2; x++ {
			off := y*img.Width + x
			ci := (y/2)*cw + x/2
			r, g, b := yuv10ToRGB(img.Y[off], img.U[ci], img.V[ci])
			dst[j+0] = expand10(r)
			dst[j+1] = expand10(g)
			dst[j+2] = expand10(b)
			dst[j+3] = 0xffff
			j += 4
		}
	}
}

// ConvertToYUV420P10LE explicitly converts img to planar yuv420p10le samples.
func ConvertToYUV420P10LE(img image.Image) (*YUV420P10LE, error) {
	if img == nil {
		return nil, errors.New("imaging: nil image")
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: empty image", ErrUnsupportedPixelFormat)
	}

	cw := (w + 1) / 2
	ch := (h + 1) / 2
	out := &YUV420P10LE{
		Width:  w,
		Height: h,
		Y:      make([]uint16, w*h),
		U:      make([]uint16, cw*ch),
		V:      make([]uint16, cw*ch),
	}
	uSum := make([]float64, cw*ch)
	vSum := make([]float64, cw*ch)
	count := make([]uint32, cw*ch)

	src := newScanner64(img)
	row := make([]uint16, w*4)
	for y := 0; y < h; y++ {
		src.scan(0, y, w, y+1, row)
		for x := 0; x < w; x++ {
			i := x * 4
			r := float64(sampleTo10(row[i])) / 1023
			g := float64(sampleTo10(row[i+1])) / 1023
			b := float64(sampleTo10(row[i+2])) / 1023

			yy := 0.2126*r + 0.7152*g + 0.0722*b
			cb := (b - yy) / 1.8556
			cr := (r - yy) / 1.5748
			out.Y[y*w+x] = uint16(clampDepth(yy*1023, 1023))

			ci := (y/2)*cw + x/2
			uSum[ci] += (cb + 0.5) * 1023
			vSum[ci] += (cr + 0.5) * 1023
			count[ci]++
		}
	}
	for i := range out.U {
		if count[i] == 0 {
			out.U[i] = 512
			out.V[i] = 512
			continue
		}
		out.U[i] = uint16(clampDepth(uSum[i]/float64(count[i]), 1023))
		out.V[i] = uint16(clampDepth(vSum[i]/float64(count[i]), 1023))
	}
	return out, nil
}

func sampleTo10(v uint16) uint16 {
	return uint16(clampDepth(float64(v)*1023/65535, 1023))
}

func yuv10ToRGB(y, u, v uint16) (uint16, uint16, uint16) {
	yy := float64(y) / 1023
	cb := (float64(u) - 512) / 1023
	cr := (float64(v) - 512) / 1023

	r := yy + 1.5748*cr
	g := yy - 0.1873*cb - 0.4681*cr
	b := yy + 1.8556*cb
	return uint16(clampDepth(r*1023, 1023)),
		uint16(clampDepth(g*1023, 1023)),
		uint16(clampDepth(b*1023, 1023))
}

// EncodeYUV420P10LE writes raw planar yuv420p10le data in Y, U, V order.
func EncodeYUV420P10LE(w io.Writer, img image.Image) error {
	src, err := ConvertToYUV420P10LE(img)
	if err != nil {
		return err
	}
	if err := writeUint16LEPlane(w, src.Y); err != nil {
		return err
	}
	if err := writeUint16LEPlane(w, src.U); err != nil {
		return err
	}
	return writeUint16LEPlane(w, src.V)
}

// DecodeYUV420P10LE reads raw planar yuv420p10le data in Y, U, V order.
func DecodeYUV420P10LE(r io.Reader, width, height int) (*YUV420P10LE, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: invalid dimensions %dx%d", ErrUnsupportedPixelFormat, width, height)
	}
	cw := (width + 1) / 2
	ch := (height + 1) / 2
	img := &YUV420P10LE{
		Width:  width,
		Height: height,
		Y:      make([]uint16, width*height),
		U:      make([]uint16, cw*ch),
		V:      make([]uint16, cw*ch),
	}
	if err := readUint16LEPlane(r, img.Y); err != nil {
		return nil, err
	}
	if err := readUint16LEPlane(r, img.U); err != nil {
		return nil, err
	}
	if err := readUint16LEPlane(r, img.V); err != nil {
		return nil, err
	}
	if err := validateYUV420P10LE(img); err != nil {
		return nil, err
	}
	return img, nil
}

func validateYUV420P10LE(img *YUV420P10LE) error {
	planes := []struct {
		name    string
		samples []uint16
	}{
		{name: "Y", samples: img.Y},
		{name: "U", samples: img.U},
		{name: "V", samples: img.V},
	}
	for _, plane := range planes {
		for i, v := range plane.samples {
			if v > 1023 {
				return fmt.Errorf("%w: %s[%d] = 0x%04x exceeds the 10-bit range",
					ErrBitDepthMismatch, plane.name, i, v)
			}
		}
	}
	return nil
}

// OpenYUV420P10LE opens a raw yuv420p10le file.
func OpenYUV420P10LE(filename string, width, height int) (*YUV420P10LE, error) {
	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return DecodeYUV420P10LE(file, width, height)
}

func writeUint16LEPlane(w io.Writer, plane []uint16) error {
	const chunkSamples = 16 << 10
	buf := make([]byte, chunkSamples*2)
	for len(plane) > 0 {
		n := len(plane)
		if n > chunkSamples {
			n = chunkSamples
		}
		chunk := plane[:n]
		for i, v := range chunk {
			binary.LittleEndian.PutUint16(buf[i*2:i*2+2], v)
		}
		if err := writeFull(w, buf[:n*2]); err != nil {
			return err
		}
		plane = plane[n:]
	}
	return nil
}

func readUint16LEPlane(r io.Reader, plane []uint16) error {
	const chunkSamples = 16 << 10
	buf := make([]byte, chunkSamples*2)
	for len(plane) > 0 {
		n := len(plane)
		if n > chunkSamples {
			n = chunkSamples
		}
		if _, err := io.ReadFull(r, buf[:n*2]); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			plane[i] = binary.LittleEndian.Uint16(buf[i*2 : i*2+2])
		}
		plane = plane[n:]
	}
	return nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
