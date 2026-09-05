//go:build !windows

package terminal

import "errors"

var ErrModalPipeUnsupported = errors.New("modal pipe transport is only available on Windows")

type ModalPipeListener struct{}

func NewModalPipeListener(*ModalServer) (*ModalPipeListener, error) {
	return nil, ErrModalPipeUnsupported
}

func NewModalPipeListenerForCapability(*ModalServer, ModalCapability) (*ModalPipeListener, error) {
	return nil, ErrModalPipeUnsupported
}

func (l *ModalPipeListener) PipeName() string { return "" }

func (l *ModalPipeListener) Serve() {}

func (l *ModalPipeListener) Close() error { return nil }
