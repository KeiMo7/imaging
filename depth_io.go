package imaging

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
)

// DecodeBitDepth sets the requested decode bit depth. The decoded pixels are
// not rounded through an 8-bit intermediate when the target is 10 or 16 bit.
func DecodeBitDepth(depth BitDepth) DecodeOption {
	return func(c *decodeConfig) {
		c.bitDepth = depth
		c.depthSet = true
	}
}

// DecodeDeclaredBitDepth declares the source depth and validates every sample.
func DecodeDeclaredBitDepth(depth BitDepth) DecodeOption {
	return func(c *decodeConfig) {
		c.declaredDepth = depth
		c.depthSet = true
	}
}

// DecodeDither sets the dither mode used by a depth-changing decode.
func DecodeDither(mode DitherMode) DecodeOption {
	return func(c *decodeConfig) {
		c.dither = mode
		c.depthSet = true
	}
}

// DecodeBitDepthLogger enables bit-depth decode logs.
func DecodeBitDepthLogger(logger BitDepthLogger) DecodeOption {
	return func(c *decodeConfig) {
		c.logger = logger
		c.depthSet = true
	}
}

// Decode10 decodes an image to a 10-bit NRGBA64 image.
func Decode10(r io.Reader, opts ...DecodeOption) (image.Image, error) {
	opts = append([]DecodeOption{DecodeBitDepth(BitDepth10)}, opts...)
	return Decode(r, opts...)
}

// Open10 opens an image as a 10-bit NRGBA64 image.
func Open10(filename string, opts ...DecodeOption) (image.Image, error) {
	opts = append([]DecodeOption{DecodeBitDepth(BitDepth10)}, opts...)
	return Open(filename, opts...)
}

// EncodeBitDepth sets the output bit depth. The zero value keeps the existing
// Encode behavior.
func EncodeBitDepth(depth BitDepth) EncodeOption {
	return func(c *encodeConfig) {
		c.bitDepth = depth
		c.depthSet = true
	}
}

// EncodeDeclaredBitDepth declares the input depth and validates every sample
// before encoding.
func EncodeDeclaredBitDepth(depth BitDepth) EncodeOption {
	return func(c *encodeConfig) {
		c.declaredDepth = depth
		c.depthSet = true
	}
}

// EncodeDither sets the dither mode used by a depth-changing encode.
func EncodeDither(mode DitherMode) EncodeOption {
	return func(c *encodeConfig) {
		c.dither = mode
		c.depthSet = true
	}
}

// EncodeBitDepthLogger enables bit-depth encode logs.
func EncodeBitDepthLogger(logger BitDepthLogger) EncodeOption {
	return func(c *encodeConfig) {
		c.logger = logger
		c.depthSet = true
	}
}

// Encode10 writes a 10-bit image. PNG and TIFF use their 16-bit container with
// exactly 10 significant bits retained; YUV420P10LE writes native 10-bit
// samples. JPEG, GIF, and BMP are rejected instead of silently dropping to 8
// bit.
func Encode10(w io.Writer, img image.Image, format Format, opts ...EncodeOption) error {
	opts = append([]EncodeOption{EncodeBitDepth(BitDepth10)}, opts...)
	return Encode(w, img, format, opts...)
}

// Save10 saves a 10-bit image atomically. The output file only appears after a
// complete successful encode.
func Save10(img image.Image, filename string, opts ...EncodeOption) error {
	opts = append([]EncodeOption{EncodeBitDepth(BitDepth10)}, opts...)
	return Save(img, filename, opts...)
}

func encodeAtDepth(w io.Writer, img image.Image, format Format, cfg encodeConfig) error {
	depthCfg := depthConfig{
		target:   cfg.bitDepth,
		declared: cfg.declaredDepth,
		dither:   cfg.dither,
		logger:   cfg.logger,
	}
	if err := depthCfg.begin("encode", img); err != nil {
		depthCfg.logFail("encode", err)
		return err
	}
	if err := validateDepthFormat(format, depthCfg.targetResolved); err != nil {
		depthCfg.logFail("encode", err)
		return err
	}

	if format == FormatYUV420P10LE {
		if err := EncodeYUV420P10LE(w, img); err != nil {
			depthCfg.logFail("encode", err)
			return err
		}
		depthCfg.logExit("encode")
		return nil
	}

	converted, err := cloneAtDepth(img, depthCfg.targetResolved, depthCfg.dither)
	if err != nil {
		depthCfg.logFail("encode", err)
		return err
	}
	if err := encodeStandard(w, converted, format, cfg); err != nil {
		depthCfg.logFail("encode", err)
		return err
	}
	depthCfg.logExit("encode")
	return nil
}

func validateDepthFormat(format Format, depth BitDepth) error {
	switch depth {
	case BitDepth8:
		if format == FormatYUV420P10LE {
			return fmt.Errorf("%w: %s requires 10-bit samples; use EncodeBitDepth(BitDepth10)",
				ErrUnsupportedBitDepth, format)
		}
		return nil
	case BitDepth10:
		switch format {
		case PNG, TIFF, FormatYUV420P10LE:
			return nil
		case JPEG, GIF, BMP:
			return fmt.Errorf("%w: %s cannot carry 10-bit samples; use PNG, TIFF, or YUV420P10LE",
				ErrUnsupportedBitDepth, format)
		}
	case BitDepth16:
		switch format {
		case PNG, TIFF:
			return nil
		case FormatYUV420P10LE:
			return fmt.Errorf("%w: %s stores 10-bit samples, not 16-bit; use PNG or TIFF",
				ErrUnsupportedBitDepth, format)
		case JPEG, GIF, BMP:
			return fmt.Errorf("%w: %s cannot carry 16-bit samples; use PNG or TIFF",
				ErrUnsupportedBitDepth, format)
		}
	}
	return fmt.Errorf("%w: %s output at %s bit", ErrUnsupportedBitDepth, format, depth)
}

func saveAtomic(img image.Image, filename string, format Format, cfg encodeConfig) (err error) {
	dir := filepath.Dir(filename)
	base := filepath.Base(filename)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := encodeWithConfig(tmp, img, format, cfg); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filename)
}
