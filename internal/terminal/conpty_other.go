//go:build !windows

package terminal

import (
	"context"
	"errors"
)

var ErrConPTYUnsupported = errors.New("ConPTY is only available on Windows")

type ConPTYBackend struct{}

func NewConPTYBackend() *ConPTYBackend { return &ConPTYBackend{} }

func (b *ConPTYBackend) Start(context.Context, Command, OutputHandler) (Process, error) {
	return nil, ErrConPTYUnsupported
}
