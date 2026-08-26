package skills

// builtinMin/builtinMax 提供与内建 min/max（Go 1.21+ 语言特性）一致的
// 实现，供调用点在双工具链下使用：Go 1.20 没有内建 min/max，
// Go 1.24 主线直接复用本实现，行为无差异（普通泛型函数，不过度遮蔽内建）。
func builtinMin[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 | ~float64 | ~string](a, b T) T {
	if a < b {
		return a
	}
	return b
}

// builtinMax 等价于内建 max：返回 a、b 中较大者。
func builtinMax[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 | ~float64 | ~string](a, b T) T {
	if a > b {
		return a
	}
	return b
}