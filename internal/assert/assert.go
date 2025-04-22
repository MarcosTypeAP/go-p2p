package assert

import (
	"fmt"
)

func True(condition bool, args ...any) {
	if condition {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: false: %s", fmt.Sprint(args...)))
	}
	panic("assert error: false")
}

func False(condition bool, args ...any) {
	if !condition {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: true: %s", fmt.Sprint(args...)))
	}
	panic("assert error: true")
}

func NoError(err error, args ...any) {
	if err == nil {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v (%s)", err, fmt.Sprint(args...)))
	}
	panic("assert error: " + err.Error())
}

func NotNil[T any](v *T, args ...any) {
	if v != nil {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: pointer is nil (%s)", fmt.Sprint(args...)))
	}
	panic("assert error: pointer is nil")
}

func Equal[N comparable](a, b N, args ...any) {
	if a == b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v != %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v != %v", a, b))
}

func NotEqual[N comparable](a, b N, args ...any) {
	if a != b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v == %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v == %v", a, b))
}

type number interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64
}

func Greater[N number](a, b N, args ...any) {
	if a > b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v <= %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v <= %v", a, b))
}

func GreaterEqual[N number](a, b N, args ...any) {
	if a >= b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v < %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v < %v", a, b))
}

func Less[N number](a, b N, args ...any) {
	if a < b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v >= %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v >= %v", a, b))
}

func LessEqual[N number](a, b N, args ...any) {
	if a <= b {
		return
	}

	if len(args) > 0 {
		panic(fmt.Sprintf("assert error: %v > %v (%s)", a, b, fmt.Sprint(args...)))
	}
	panic(fmt.Sprintf("assert error: %v > %v", a, b))
}
