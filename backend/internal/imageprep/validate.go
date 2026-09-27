package imageprep

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxValidateBytes bounds the file read ValidateImageFile performs. It sits
// above DefaultMaxBytes so every image the attach path may forward is covered.
const maxValidateBytes = 64 << 20

// ValidateImageData reports whether data really is a complete image of the
// declared MIME. DecodeConfig only reads the header, so a payload with a valid
// header and no pixel data ("iVBORw0KGgo=" or an IHDR+IEND PNG) passed every
// earlier check and was advertised as an attachable image; a strict provider
// then rejected the whole request instead of the tool degrading gracefully
// (2026-09-27 review H10).
//
// The walk validates the container layout only (no pixel decode, no large
// allocations): PNG requires IHDR + at least one IDAT + IEND, JPEG requires SOI
// + a frame header + EOI, GIF requires a header + at least one image descriptor
// + trailer. Legal files always satisfy these.
func ValidateImageData(data []byte, mime string) error {
	switch sanitizeValidateMime(mime) {
	case "image/png":
		return validatePNG(data)
	case "image/jpeg":
		return validateJPEG(data)
	case "image/gif":
		return validateGIF(data)
	}
	return fmt.Errorf("unsupported image type %q", mime)
}

// ValidateImageFile is ValidateImageData for a path, reading at most
// maxValidateBytes. Missing/oversized files are reported as errors so callers
// fall back to a text/note path instead of attaching unknown bytes.
func ValidateImageFile(path string, mime string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxValidateBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxValidateBytes {
		return fmt.Errorf("图片超过校验上限 %d 字节", maxValidateBytes)
	}
	return ValidateImageData(data, mime)
}

func sanitizeValidateMime(mime string) string {
	return strings.ToLower(strings.TrimSpace(mime))
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

func validatePNG(data []byte) error {
	if len(data) < len(pngSignature) || !bytes.Equal(data[:len(pngSignature)], pngSignature) {
		return errors.New("不是 PNG（签名不匹配）")
	}
	offset := len(pngSignature)
	var seenIHDR, seenIDAT, seenIEND bool
	for offset+8 <= len(data) {
		length := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		kind := string(data[offset+4 : offset+8])
		end := int64(offset) + 8 + length + 4
		if end > int64(len(data)) {
			return fmt.Errorf("PNG 数据被截断（chunk %s 长度不足）", kind)
		}
		switch kind {
		case "IHDR":
			seenIHDR = true
		case "IDAT":
			seenIDAT = true
		case "IEND":
			seenIEND = true
		}
		offset = int(end)
		if kind == "IEND" {
			break
		}
	}
	if !seenIHDR || !seenIDAT || !seenIEND {
		return fmt.Errorf("PNG 不完整（IHDR=%t IDAT=%t IEND=%t）", seenIHDR, seenIDAT, seenIEND)
	}
	return nil
}

func validateJPEG(data []byte) error {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return errors.New("不是 JPEG（缺少 SOI）")
	}
	offset := 2
	seenFrame := false
	for offset < len(data) {
		for offset < len(data) && data[offset] == 0xFF {
			offset++
		}
		if offset >= len(data) {
			break
		}
		marker := data[offset]
		offset++
		switch {
		case marker == 0xD9: // EOI
			if !seenFrame {
				return errors.New("JPEG 缺少帧头（SOF）")
			}
			return nil
		case marker == 0x01, marker >= 0xD0 && marker <= 0xD7: // standalone markers
			continue
		case marker == 0xDA: // start of scan: entropy data runs to EOI
			if !seenFrame {
				return errors.New("JPEG 缺少帧头（SOF）")
			}
			if !bytes.Contains(data[offset:], []byte{0xFF, 0xD9}) {
				return errors.New("JPEG 数据被截断（缺少 EOI）")
			}
			return nil
		default:
			if offset+2 > len(data) {
				return errors.New("JPEG 数据被截断（段长度缺失）")
			}
			segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
			if segmentLength < 2 || offset+segmentLength > len(data) {
				return errors.New("JPEG 数据被截断（段不完整）")
			}
			if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				seenFrame = true
			}
			offset += segmentLength
		}
	}
	return errors.New("JPEG 数据被截断（未找到 EOI）")
}

func validateGIF(data []byte) error {
	if len(data) < 13 {
		return errors.New("GIF 头部不完整")
	}
	header := string(data[:6])
	if header != "GIF87a" && header != "GIF89a" {
		return errors.New("不是 GIF（版本标识不匹配）")
	}
	offset := 13
	if data[10]&0x80 != 0 { // global color table
		offset += 3 * (1 << ((data[10] & 0x07) + 1))
	}
	seenImage := false
	for offset < len(data) {
		switch data[offset] {
		case 0x3B: // trailer
			if !seenImage {
				return errors.New("GIF 没有图像数据")
			}
			return nil
		case 0x2C: // image descriptor
			if offset+10 > len(data) {
				return errors.New("GIF 图像描述符被截断")
			}
			seenImage = true
			localFlags := data[offset+9]
			offset += 10
			if localFlags&0x80 != 0 {
				offset += 3 * (1 << ((localFlags & 0x07) + 1))
			}
			if offset >= len(data) {
				return errors.New("GIF 图像数据被截断")
			}
			offset++ // LZW minimum code size
			next, err := skipGIFSubBlocks(data, offset)
			if err != nil {
				return err
			}
			offset = next
		case 0x21: // extension block
			if offset+2 > len(data) {
				return errors.New("GIF 扩展块被截断")
			}
			offset += 2
			next, err := skipGIFSubBlocks(data, offset)
			if err != nil {
				return err
			}
			offset = next
		default:
			return fmt.Errorf("GIF 块结构异常（0x%02X）", data[offset])
		}
	}
	return errors.New("GIF 数据被截断（缺少 trailer）")
}

// skipGIFSubBlocks walks a GIF sub-block chain (length byte + payload,
// terminated by a zero-length block) and returns the offset after it.
func skipGIFSubBlocks(data []byte, offset int) (int, error) {
	for {
		if offset >= len(data) {
			return 0, errors.New("GIF 子块被截断")
		}
		size := int(data[offset])
		offset++
		if size == 0 {
			return offset, nil
		}
		if offset+size > len(data) {
			return 0, errors.New("GIF 子块被截断")
		}
		offset += size
	}
}
