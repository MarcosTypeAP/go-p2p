package expect

import (
	"fmt"
	"slices"
	"testing"
)

func True(tb testing.TB, condition bool, args ...any) {
	if condition {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected false: %s", fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected false")
}

func False(tb testing.TB, condition bool, args ...any) {
	if !condition {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected true: %s", fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected true")
}

func NoError(tb testing.TB, err error, args ...any) {
	if err == nil {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected no error: %v (%s)", err, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected no error: %v", err)
}

func NotNil[T any](tb testing.TB, v *T, args ...any) {
	if v != nil {
		return
	}

	if len(args) > 0 {
		tb.Fatalf("expected not nil: %s", fmt.Sprint(args...))
		return
	}
	tb.Fatal("expected not nil")
}

func Equal[T comparable](tb testing.TB, a, b T, args ...any) {
	if a == b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v == %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v == %v", a, b)
}

func NotEqual[T comparable](tb testing.TB, a, b T, args ...any) {
	if a != b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v != %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v != %v", a, b)
}

func SlicesEqual[T comparable](tb testing.TB, a, b []T, args ...any) {
	if slices.Equal(a, b) {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v == %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v == %v", a, b)
}

type number interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64
}

func Greater[N number](tb testing.TB, a, b N, args ...any) {
	if a > b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v > %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v > %v", a, b)
}

func GreaterEqual[N number](tb testing.TB, a, b N, args ...any) {
	if a >= b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v >= %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v >= %v", a, b)
}

func Less[N number](tb testing.TB, a, b N, args ...any) {
	if a < b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v < %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v < %v", a, b)
}

func LessEqual[N number](tb testing.TB, a, b N, args ...any) {
	if a <= b {
		return
	}

	tb.Helper()

	if len(args) > 0 {
		tb.Fatalf("expected %v <= %v (%s)", a, b, fmt.Sprint(args...))
		return
	}
	tb.Fatalf("expected %v <= %v", a, b)
}
