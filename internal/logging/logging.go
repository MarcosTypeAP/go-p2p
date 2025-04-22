package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"

	"github.com/MarcosTypeAP/go-p2p/internal/assert"
	"github.com/MarcosTypeAP/go-p2p/internal/style"
)

var bufPool = sync.Pool{New: func() any {
	return bytes.NewBuffer(make([]byte, 0, 128))
}}

type LogginHandler struct {
	opts *slog.HandlerOptions
	w    io.Writer
}

func NewLoggingHandler(w io.Writer, opts *slog.HandlerOptions) *LogginHandler {
	return &LogginHandler{
		w:    w,
		opts: opts,
	}
}

func (h *LogginHandler) Handle(_ context.Context, r slog.Record) error {
	buf := bufPool.Get().(*bytes.Buffer)
	defer bufPool.Put(buf)

	buf.WriteString(r.Time.Format("15:04:20.000000") + " ")

	var keysColor style.Style
	switch r.Level {
	case slog.LevelDebug:
		keysColor = style.Green
		buf.WriteString(style.Text("DEBUG", style.Bold, keysColor))
	case slog.LevelInfo:
		keysColor = style.Blue
		buf.WriteString(style.Text("INFO ", style.Bold, keysColor))
	case slog.LevelWarn:
		keysColor = style.Yellow
		buf.WriteString(style.Text("WARN ", style.Bold, keysColor))
	case slog.LevelError:
		keysColor = style.Red
		buf.WriteString(style.Text("ERROR", style.Bold, keysColor))
	default:
		keysColor = style.White
		buf.WriteString(style.Text(r.Level.String(), style.Bold, keysColor))
	}

	buf.WriteString(" " + r.Message + " ")

	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "err" {
			buf.WriteString(style.Text(a.Key, keysColor) + `="` + style.Text(a.Value.String(), keysColor) + `" `)
		} else {
			buf.WriteString(style.Text(a.Key, keysColor) + `="` + a.Value.String() + `" `)
		}
		return true
	})

	if h.opts.AddSource {
		source := h.getSource(r)
		buf.WriteString("" +
			source.File +
			":" +
			style.Text(fmt.Sprint(source.Line), style.Yellow) +
			" " +
			style.Text(source.Function, style.Blue) +
			" ",
		)
	}

	buf.UnreadByte()
	buf.WriteByte('\n')

	_, err := buf.WriteTo(h.w)
	assert.NoError(err)

	assert.Less(buf.Cap(), 1024*1024)

	return nil
}

func (h LogginHandler) getSource(r slog.Record) *slog.Source {
	fs := runtime.CallersFrames([]uintptr{r.PC})
	f, _ := fs.Next()

	return &slog.Source{
		Function: f.Function,
		File:     f.File,
		Line:     f.Line,
	}
}

func (h *LogginHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *LogginHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	panic("not implemented")
}

func (h *LogginHandler) WithGroup(group string) slog.Handler {
	panic("not implemented")
}
