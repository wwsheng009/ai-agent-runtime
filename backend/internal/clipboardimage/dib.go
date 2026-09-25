package clipboardimage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
)

const (
	dibHeaderSize = 40
	biRGB         = 0
	biBitFields   = 3
)

// decodeDIB 解析 Windows CF_DIB / CF_DIBV5 的 BITMAPINFO 与像素数据。
//
// 支持：
//   - 24 位 BI_RGB（BGR，行按 4 字节对齐）；
//   - 32 位 BI_RGB（BGRA，Windows 常把 alpha 字节写 0 —— 全 0 时按不透明处理）；
//   - 32 位 BI_BITFIELDS（V5 头内掩码，或 40 字节头后紧跟 3 个 DWORD 掩码）；
//   - 自下而上（正高度）与自上而下（负高度）两种行序。
//
// 不支持 8 位及以下调色板位图（剪贴板极少出现，遇到时明确报错而不是猜）。
func decodeDIB(data []byte) (image.Image, error) {
	if len(data) < dibHeaderSize {
		return nil, fmt.Errorf("DIB 数据过短: %d 字节", len(data))
	}
	headerSize := int(binary.LittleEndian.Uint32(data[0:4]))
	if headerSize < dibHeaderSize || headerSize > len(data) {
		return nil, fmt.Errorf("DIB 头部尺寸异常: %d", headerSize)
	}
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	heightRaw := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	if width <= 0 || heightRaw == 0 {
		return nil, fmt.Errorf("DIB 尺寸非法: %d x %d", width, heightRaw)
	}
	topDown := heightRaw < 0
	height := heightRaw
	if topDown {
		height = -heightRaw
	}
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := int(binary.LittleEndian.Uint32(data[16:20]))
	if bitCount != 24 && bitCount != 32 {
		return nil, fmt.Errorf("不支持的 DIB 位深: %d（仅支持 24/32 位）", bitCount)
	}

	var masks [4]uint32
	hasMasks := false
	offset := headerSize
	switch compression {
	case biRGB:
		// V5 头（>=56 字节）即使声明 BI_RGB 也自带掩码，取到就用。
		if headerSize >= 56 {
			masks[0] = binary.LittleEndian.Uint32(data[40:44])
			masks[1] = binary.LittleEndian.Uint32(data[44:48])
			masks[2] = binary.LittleEndian.Uint32(data[48:52])
			masks[3] = binary.LittleEndian.Uint32(data[52:56])
			hasMasks = masks[0]|masks[1]|masks[2] != 0
		}
	case biBitFields:
		if headerSize >= 56 {
			masks[0] = binary.LittleEndian.Uint32(data[40:44])
			masks[1] = binary.LittleEndian.Uint32(data[44:48])
			masks[2] = binary.LittleEndian.Uint32(data[48:52])
			masks[3] = binary.LittleEndian.Uint32(data[52:56])
		} else {
			if len(data) < headerSize+12 {
				return nil, errors.New("DIB 掩码数据不完整")
			}
			masks[0] = binary.LittleEndian.Uint32(data[headerSize : headerSize+4])
			masks[1] = binary.LittleEndian.Uint32(data[headerSize+4 : headerSize+8])
			masks[2] = binary.LittleEndian.Uint32(data[headerSize+8 : headerSize+12])
			offset = headerSize + 12
		}
		hasMasks = masks[0]|masks[1]|masks[2] != 0
	default:
		return nil, fmt.Errorf("不支持的 DIB 压缩方式: %d", compression)
	}
	if compression == biBitFields && bitCount != 32 {
		return nil, fmt.Errorf("BI_BITFIELDS 仅支持 32 位，收到 %d 位", bitCount)
	}

	// 部分生产者（例如 .NET/GDI+ 写 CF_DIBV5）在 V5 头之后又重复写了一份经典
	// 3 DWORD 掩码块：此时像素起点要再往后挪 12 字节，否则整幅图会错位。
	if compression == biBitFields && headerSize >= 56 && len(data) >= headerSize+12 {
		duplicate := [3]uint32{
			binary.LittleEndian.Uint32(data[headerSize : headerSize+4]),
			binary.LittleEndian.Uint32(data[headerSize+4 : headerSize+8]),
			binary.LittleEndian.Uint32(data[headerSize+8 : headerSize+12]),
		}
		if duplicate == [3]uint32{masks[0], masks[1], masks[2]} {
			offset = headerSize + 12
		}
	}

	stride := ((width*bitCount + 31) / 32) * 4
	need := offset + stride*height
	if need > len(data) {
		// 掩码块的判断有误时，退一步尝试另一种偏移，避免整幅图直接失败。
		alternative := headerSize
		if offset != headerSize {
			alternative = headerSize
		} else if headerSize >= 40 && len(data) >= headerSize+12 {
			alternative = headerSize + 12
		}
		if alternative+stride*height <= len(data) {
			offset = alternative
			need = alternative + stride*height
		} else {
			return nil, fmt.Errorf("DIB 像素数据截断: 需要 %d 字节，实际 %d", need, len(data))
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	alphaAllZero := bitCount == 32
	for y := 0; y < height; y++ {
		sourceY := y
		if !topDown {
			sourceY = height - 1 - y
		}
		row := data[offset+sourceY*stride:]
		for x := 0; x < width; x++ {
			base := x * bitCount / 8
			var r, g, b, a uint8
			if bitCount == 32 {
				value := binary.LittleEndian.Uint32(row[base : base+4])
				if hasMasks {
					r = shiftMask(value, masks[0])
					g = shiftMask(value, masks[1])
					b = shiftMask(value, masks[2])
					if masks[3] != 0 {
						a = shiftMask(value, masks[3])
					} else {
						a = 255
					}
				} else {
					b = row[base]
					g = row[base+1]
					r = row[base+2]
					a = row[base+3]
				}
				if a != 0 {
					alphaAllZero = false
				}
			} else {
				b = row[base]
				g = row[base+1]
				r = row[base+2]
				a = 255
			}
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}
	if alphaAllZero {
		// Windows 在 CF_DIB 里经常把 alpha 字节留 0；整幅全 0 视为不透明，
		// 避免把用户粘贴的截图变成全透明图片。
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				pixel := img.NRGBAAt(x, y)
				pixel.A = 255
				img.SetNRGBA(x, y, pixel)
			}
		}
	}
	return img, nil
}

// shiftMask 按掩码把原始通道值归一化到 0-255。
func shiftMask(value, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := uint(0)
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	channel := (value >> shift) & mask
	if mask == 0 {
		return 0
	}
	return uint8(channel * 255 / mask)
}
