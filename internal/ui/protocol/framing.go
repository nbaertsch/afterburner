package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

const (
	DefaultMaxFrameBytes uint32 = 1 << 20
	DefaultMaxJSONDepth         = 64
	LengthPrefixBytes           = 4
)

type FramingOptions struct {
	MaxFrameBytes uint32
	MaxJSONDepth  int
}

type Stream struct {
	rw      io.ReadWriter
	options FramingOptions
	readMu  chan struct{}
	writeMu chan struct{}
}

func NewStream(rw io.ReadWriter, options FramingOptions) *Stream {
	return &Stream{rw: rw, options: normalizeFramingOptions(options), readMu: make(chan struct{}, 1), writeMu: make(chan struct{}, 1)}
}

func (s *Stream) ReadEnvelope(ctx context.Context) (Envelope, error) {
	var envelope Envelope
	if err := s.ReadJSON(ctx, &envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (s *Stream) WriteEnvelope(ctx context.Context, envelope Envelope) error {
	return s.WriteJSON(ctx, envelope)
}

func (s *Stream) ReadJSON(ctx context.Context, dst any) error {
	if err := acquire(ctx, s.readMu); err != nil {
		return err
	}
	defer release(s.readMu)
	return ReadJSONFrame(ctx, s.rw, dst, s.options)
}

func (s *Stream) WriteJSON(ctx context.Context, value any) error {
	if err := acquire(ctx, s.writeMu); err != nil {
		return err
	}
	defer release(s.writeMu)
	return WriteJSONFrame(ctx, s.rw, value, s.options)
}

func (s *Stream) ReadFrame(ctx context.Context) ([]byte, error) {
	if err := acquire(ctx, s.readMu); err != nil {
		return nil, err
	}
	defer release(s.readMu)
	return ReadFrame(ctx, s.rw, s.options)
}

func (s *Stream) WriteFrame(ctx context.Context, payload []byte) error {
	if err := acquire(ctx, s.writeMu); err != nil {
		return err
	}
	defer release(s.writeMu)
	return WriteFrame(ctx, s.rw, payload, s.options)
}

func ReadJSONFrame(ctx context.Context, r io.Reader, dst any, options FramingOptions) error {
	payload, err := ReadFrame(ctx, r, options)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return StructuredError{Code: ErrorInvalidJSON, Message: "frame JSON does not match target type", Recoverable: false}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return StructuredError{Code: ErrorInvalidJSON, Message: "frame contains trailing JSON", Recoverable: false}
	}
	return nil
}

func WriteJSONFrame(ctx context.Context, w io.Writer, value any, options FramingOptions) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return StructuredError{Code: ErrorInvalidJSON, Message: "frame JSON cannot be encoded", Recoverable: false}
	}
	return WriteFrame(ctx, w, payload, options)
}

func ReadFrame(ctx context.Context, r io.Reader, options FramingOptions) ([]byte, error) {
	ctx = backgroundIfNil(ctx)
	options = normalizeFramingOptions(options)
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	clear := applyReadDeadline(ctx, r)
	defer clear()
	var prefix [LengthPrefixBytes]byte
	if _, err := io.ReadFull(contextReader{ctx: ctx, r: r}, prefix[:]); err != nil {
		return nil, mapIOError(ctx, err, ErrorInvalidFrame, "could not read frame length")
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 {
		return nil, StructuredError{Code: ErrorInvalidFrame, Message: "empty frames are not allowed", Recoverable: false}
	}
	if length > options.MaxFrameBytes {
		return nil, StructuredError{Code: ErrorFrameTooLarge, Message: "frame exceeds configured byte limit", Recoverable: false, Details: map[string]string{"limitBytes": fmt.Sprint(options.MaxFrameBytes)}}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(contextReader{ctx: ctx, r: r}, payload); err != nil {
		return nil, mapIOError(ctx, err, ErrorInvalidFrame, "could not read complete frame")
	}
	if !utf8.Valid(payload) {
		return nil, StructuredError{Code: ErrorInvalidJSON, Message: "frame is not valid UTF-8", Recoverable: false}
	}
	if err := ValidateJSONDepth(payload, options.MaxJSONDepth); err != nil {
		return nil, err
	}
	return payload, nil
}

func WriteFrame(ctx context.Context, w io.Writer, payload []byte, options FramingOptions) error {
	ctx = backgroundIfNil(ctx)
	options = normalizeFramingOptions(options)
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if len(payload) == 0 {
		return StructuredError{Code: ErrorInvalidFrame, Message: "empty frames are not allowed", Recoverable: false}
	}
	if !utf8.Valid(payload) {
		return StructuredError{Code: ErrorInvalidJSON, Message: "frame is not valid UTF-8", Recoverable: false}
	}
	if uint64(len(payload)) > uint64(options.MaxFrameBytes) {
		return StructuredError{Code: ErrorFrameTooLarge, Message: "frame exceeds configured byte limit", Recoverable: false, Details: map[string]string{"limitBytes": fmt.Sprint(options.MaxFrameBytes)}}
	}
	if err := ValidateJSONDepth(payload, options.MaxJSONDepth); err != nil {
		return err
	}
	clear := applyWriteDeadline(ctx, w)
	defer clear()
	var prefix [LengthPrefixBytes]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(payload)))
	if err := writeFull(ctx, w, prefix[:]); err != nil {
		return mapIOError(ctx, err, ErrorInvalidFrame, "could not write frame length")
	}
	if err := writeFull(ctx, w, payload); err != nil {
		return mapIOError(ctx, err, ErrorInvalidFrame, "could not write complete frame")
	}
	return nil
}

func ValidateJSONDepth(payload []byte, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxJSONDepth
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return StructuredError{Code: ErrorInvalidJSON, Message: "frame JSON is malformed", Recoverable: false}
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
				if depth > maxDepth {
					return StructuredError{Code: ErrorJSONTooDeep, Message: "frame JSON exceeds configured depth limit", Recoverable: false, Details: map[string]string{"limitDepth": fmt.Sprint(maxDepth)}}
				}
			case '}', ']':
				depth--
			}
		}
	}
}

func normalizeFramingOptions(options FramingOptions) FramingOptions {
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if options.MaxJSONDepth == 0 {
		options.MaxJSONDepth = DefaultMaxJSONDepth
	}
	return options
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	ctx = backgroundIfNil(ctx)
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctxErr(ctx)
	}
}

func release(semaphore chan struct{}) { <-semaphore }

func writeFull(ctx context.Context, w io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := ctxErr(ctx); err != nil {
			return err
		}
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := ctxErr(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if err != nil {
		return n, err
	}
	if err := ctxErr(r.ctx); err != nil {
		return n, err
	}
	return n, nil
}

func backgroundIfNil(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	switch err := ctx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		return StructuredError{Code: ErrorDeadlineExceeded, Message: "operation deadline exceeded", Recoverable: true}
	case errors.Is(err, context.Canceled):
		return StructuredError{Code: ErrorCanceled, Message: "operation canceled", Recoverable: true}
	default:
		return nil
	}
}

func mapIOError(ctx context.Context, err error, code ErrorCode, message string) error {
	if ctxError := ctxErr(ctx); ctxError != nil {
		return ctxError
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return StructuredError{Code: ErrorDeadlineExceeded, Message: "operation deadline exceeded", Recoverable: true}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ctxErr(ctx)
	}
	return StructuredError{Code: code, Message: message, Recoverable: false}
}

type readDeadliner interface{ SetReadDeadline(time.Time) error }
type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

func applyReadDeadline(ctx context.Context, r io.Reader) func() {
	deadline, ok := ctx.Deadline()
	if !ok {
		return func() {}
	}
	if conn, ok := r.(readDeadliner); ok {
		_ = conn.SetReadDeadline(deadline)
		return func() { _ = conn.SetReadDeadline(time.Time{}) }
	}
	return func() {}
}

func applyWriteDeadline(ctx context.Context, w io.Writer) func() {
	deadline, ok := ctx.Deadline()
	if !ok {
		return func() {}
	}
	if conn, ok := w.(writeDeadliner); ok {
		_ = conn.SetWriteDeadline(deadline)
		return func() { _ = conn.SetWriteDeadline(time.Time{}) }
	}
	return func() {}
}
