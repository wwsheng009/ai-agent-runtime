//go:build go1.20 && !go1.21

package ui

// Go 1.21 起 min/max 成为内建函数；本文件为 Go 1.20 兼容构建（Windows 7
// 目标）提供等价实现。仅在 go1.20 且非 go1.21+ 工具链下编译，不影响
// 主线（go 1.24）构建。

type ordered20 interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64 | ~string
}

func min[T ordered20](v ...T) T {
	if len(v) == 0 {
		var zero T
		return zero
	}
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func max[T ordered20](v ...T) T {
	if len(v) == 0 {
		var zero T
		return zero
	}
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}