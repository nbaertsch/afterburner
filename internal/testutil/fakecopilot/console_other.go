//go:build !windows

package main

func prepareInterruptInput() func() { return func() {} }
