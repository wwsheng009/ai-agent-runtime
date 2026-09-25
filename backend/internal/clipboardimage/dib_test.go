package clipboardimage

import (
	"encoding/binary"
	"image"
	"testing"
)

// buildDIB 按 BITMAPINFOHEADER 组装测试用的 DIB：pixels 为自上而下的 BGR(A) 行数据，
// bottomUp 为 true 时按 Windows 常见行序（最后一行是图像第一行）写回。
func buildDIB(width, height, bitCount int, compression uint32, masks [4]uint32, pixels [][]byte, bottomUp bool) []byte {
	return buildDIBWithHeader(width, height, bitCount, compression, masks, pixels, bottomUp, 40, false)
}

// buildDIBWithHeader 允许指定头部尺寸：40 字节（CF_DIB，BI_BITFIELDS 时掩码在头后）
// 或 56/124 字节（V5 头，掩码在头内 40..56 字节处）；duplicateMasks 为 true 时
// 额外模拟 .NET/GDI+ 的行为：在 V5 头之后重复写一份 12 字节掩码块。
func buildDIBWithHeader(width, height, bitCount int, compression uint32, masks [4]uint32, pixels [][]byte, bottomUp bool, headerSize int, duplicateMasks bool) []byte {
	stride := ((width*bitCount + 31) / 32) * 4
	extra := 0
	if compression == biBitFields && headerSize < 56 {
		extra = 12
	} else if duplicateMasks {
		extra = 12
	}
	data := make([]byte, headerSize+extra+stride*height)
	binary.LittleEndian.PutUint32(data[0:4], uint32(headerSize))
	binary.LittleEndian.PutUint32(data[4:8], uint32(int32(width)))
	heightValue := int32(height)
	if !bottomUp {
		heightValue = -heightValue
	}
	binary.LittleEndian.PutUint32(data[8:12], uint32(heightValue))
	binary.LittleEndian.PutUint16(data[12:14], 1)
	binary.LittleEndian.PutUint16(data[14:16], uint16(bitCount))
	binary.LittleEndian.PutUint32(data[16:20], compression)
	offset := headerSize
	if compression == biBitFields && headerSize < 56 {
		binary.LittleEndian.PutUint32(data[40:44], masks[0])
		binary.LittleEndian.PutUint32(data[44:48], masks[1])
		binary.LittleEndian.PutUint32(data[48:52], masks[2])
		offset = 52
	} else if headerSize >= 56 {
		binary.LittleEndian.PutUint32(data[40:44], masks[0])
		binary.LittleEndian.PutUint32(data[44:48], masks[1])
		binary.LittleEndian.PutUint32(data[48:52], masks[2])
		binary.LittleEndian.PutUint32(data[52:56], masks[3])
		if duplicateMasks {
			binary.LittleEndian.PutUint32(data[headerSize:headerSize+4], masks[0])
			binary.LittleEndian.PutUint32(data[headerSize+4:headerSize+8], masks[1])
			binary.LittleEndian.PutUint32(data[headerSize+8:headerSize+12], masks[2])
			offset = headerSize + 12
		}
	}
	for y := 0; y < height; y++ {
		sourceY := y
		if bottomUp {
			sourceY = height - 1 - y
		}
		copy(data[offset+y*stride:], pixels[sourceY])
	}
	return data
}

func TestDecodeDIB24BitBottomUpWithPadding(t *testing.T) {
	// 宽 3 的 24 位行按 4 字节对齐，每行有 3 字节 padding。
	red := []byte{0x00, 0x00, 0xFF, 0x00, 0x00, 0xFF, 0x00, 0x00, 0xFF, 0, 0, 0}
	blue := []byte{0xFF, 0x00, 0x00, 0xFF, 0x00, 0x00, 0xFF, 0x00, 0x00, 0, 0, 0}
	dib := buildDIB(3, 2, 24, biRGB, [4]uint32{}, [][]byte{red, blue}, true)

	decoded, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB 失败: %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 3 || bounds.Dy() != 2 {
		t.Fatalf("尺寸错误: %v", bounds)
	}
	// 自下而上：数据最后一行是图像第一行（red）。
	if r, g, b, a := decoded.At(0, 0).RGBA(); r != 0xFFFF || g != 0 || b != 0 || a != 0xFFFF {
		t.Fatalf("(0,0) 应为红色，得到 r=%d g=%d b=%d a=%d", r, g, b, a)
	}
	if r, g, b, _ := decoded.At(1, 1).RGBA(); r != 0 || g != 0 || b != 0xFFFF {
		t.Fatalf("(1,1) 应为蓝色，得到 r=%d g=%d b=%d", r, g, b)
	}
}

func TestDecodeDIB32BitAlphaZeroBecomesOpaque(t *testing.T) {
	// 32 位行：BGRA，alpha 字节全 0（Windows 常见），应回退为不透明。
	row := []byte{0x10, 0x20, 0x30, 0x00}
	dib := buildDIB(1, 1, 32, biRGB, [4]uint32{}, [][]byte{row}, false)

	decoded, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB 失败: %v", err)
	}
	r, g, b, a := decoded.At(0, 0).RGBA()
	if r>>8 != 0x30 || g>>8 != 0x20 || b>>8 != 0x10 {
		t.Fatalf("通道顺序错误: r=%02x g=%02x b=%02x", r>>8, g>>8, b>>8)
	}
	if a>>8 != 0xFF {
		t.Fatalf("alpha 全 0 时应转为不透明，得到 %02x", a>>8)
	}
}

func TestDecodeDIBBitFieldsWithAlpha(t *testing.T) {
	// 40 字节头 + 3 个 DWORD 掩码：没有 alpha 掩码，alpha 必须按不透明处理。
	masks := [4]uint32{0x00FF0000, 0x0000FF00, 0x000000FF, 0xFF000000}
	row := []byte{0x33, 0x22, 0x11, 0x80}
	dib := buildDIB(1, 1, 32, biBitFields, masks, [][]byte{row}, false)

	decoded, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB 失败: %v", err)
	}
	r, g, b, a := decoded.At(0, 0).RGBA()
	if r>>8 != 0x11 || g>>8 != 0x22 || b>>8 != 0x33 {
		t.Fatalf("掩码解析错误: r=%02x g=%02x b=%02x", r>>8, g>>8, b>>8)
	}
	if a>>8 != 0xFF {
		t.Fatalf("无 alpha 掩码时 alpha 应为不透明: %02x", a>>8)
	}
}

func TestDecodeDIBV5AlphaMask(t *testing.T) {
	// V5 头（124 字节）掩码在头内，含 alpha：像素 0x80112233 → b=0x33 g=0x22 r=0x11 a=0x80。
	masks := [4]uint32{0x00FF0000, 0x0000FF00, 0x000000FF, 0xFF000000}
	row := []byte{0x33, 0x22, 0x11, 0x80}
	dib := buildDIBWithHeader(1, 1, 32, biBitFields, masks, [][]byte{row}, false, 124, false)

	decoded, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB 失败: %v", err)
	}
	// 带 alpha 时 At().RGBA() 返回预乘值，这里直接断言 NRGBA 原始通道。
	nrgba, ok := decoded.(*image.NRGBA)
	if !ok {
		t.Fatalf("解码结果类型应为 *image.NRGBA，得到 %T", decoded)
	}
	pixel := nrgba.NRGBAAt(0, 0)
	if pixel.R != 0x11 || pixel.G != 0x22 || pixel.B != 0x33 || pixel.A != 0x80 {
		t.Fatalf("V5 掩码/alpha 解析错误: %+v", pixel)
	}
}

func TestDecodeDIBV5WithDuplicateMaskBlock(t *testing.T) {
	// .NET/GDI+ 写 CF_DIBV5 时会在 124 字节头之后再重复一份 3 DWORD 掩码块，
	// 像素起点因此是 136。用 2x1 图验证不会整体错位（真实剪贴板 E2E 发现的回归）。
	masks := [4]uint32{0x00FF0000, 0x0000FF00, 0x000000FF, 0x00000000}
	row := []byte{
		0x33, 0x22, 0x11, 0x00, // 像素 0 → r=0x11 g=0x22 b=0x33
		0xAA, 0xBB, 0xCC, 0x00, // 像素 1 → r=0xCC g=0xBB b=0xAA
	}
	dib := buildDIBWithHeader(2, 1, 32, biBitFields, masks, [][]byte{row}, false, 124, true)

	decoded, err := decodeDIB(dib)
	if err != nil {
		t.Fatalf("decodeDIB 失败: %v", err)
	}
	nrgba, ok := decoded.(*image.NRGBA)
	if !ok {
		t.Fatalf("解码结果类型应为 *image.NRGBA，得到 %T", decoded)
	}
	first := nrgba.NRGBAAt(0, 0)
	if first.R != 0x11 || first.G != 0x22 || first.B != 0x33 {
		t.Fatalf("像素 0 解析错误（疑似像素起点未跳过重复掩码块）: %+v", first)
	}
	second := nrgba.NRGBAAt(1, 0)
	if second.R != 0xCC || second.G != 0xBB || second.B != 0xAA {
		t.Fatalf("像素 1 解析错误: %+v", second)
	}
}

func TestDecodeDIBRejectsUnsupportedAndTruncated(t *testing.T) {
	if _, err := decodeDIB(make([]byte, 10)); err == nil {
		t.Fatal("过短数据应报错")
	}
	eightBit := buildDIB(1, 1, 24, biRGB, [4]uint32{}, [][]byte{{1, 2, 3, 0}}, false)
	binary.LittleEndian.PutUint16(eightBit[14:16], 8)
	if _, err := decodeDIB(eightBit); err == nil {
		t.Fatal("8 位位深应明确报错")
	}
	truncated := buildDIB(4, 4, 32, biRGB, [4]uint32{}, [][]byte{
		{1, 2, 3, 4}, {1, 2, 3, 4}, {1, 2, 3, 4}, {1, 2, 3, 4},
	}, false)
	if _, err := decodeDIB(truncated[:len(truncated)-8]); err == nil {
		t.Fatal("截断的像素数据应报错")
	}
	badCompression := buildDIB(1, 1, 24, biRGB, [4]uint32{}, [][]byte{{1, 2, 3, 0}}, false)
	binary.LittleEndian.PutUint32(badCompression[16:20], 5)
	if _, err := decodeDIB(badCompression); err == nil {
		t.Fatal("未知压缩方式应报错")
	}
}

func TestAvailabilityReportsReason(t *testing.T) {
	available, reason := Availability()
	if reason == "" {
		t.Fatal("能力查询必须给出原因（供 /hotkeys 展示）")
	}
	_ = available
}

func TestShiftMask(t *testing.T) {
	if got := shiftMask(0x000000FF, 0x000000FF); got != 0xFF {
		t.Fatalf("低 8 位掩码应为 0xFF，得到 %02x", got)
	}
	if got := shiftMask(0x00FF0000, 0x00FF0000); got != 0xFF {
		t.Fatalf("中 8 位掩码应为 0xFF，得到 %02x", got)
	}
	if got := shiftMask(0x11223344, 0xFF000000); got != 0x11 {
		t.Fatalf("高位掩码应为 0x11，得到 %02x", got)
	}
}
