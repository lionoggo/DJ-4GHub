//go:build !linux && (!darwin || !cgo)

package main

import (
	"errors"
	"time"
)

type usbAT struct{}

func openDJIUSBAT() (*usbAT, error) {
	return nil, errors.New("USB AT requires a macOS cgo build with libusb or a Linux build")
}

func (u *usbAT) Close() {}

func (u *usbAT) Description() string { return "USB AT is unavailable" }

func (u *usbAT) Command(_ string, _ time.Duration) (string, error) {
	return "", errors.New("USB AT is unavailable in this build")
}

func (u *usbAT) CommandWithPrompt(_ string, _ []byte, _ time.Duration) (string, error) {
	return "", errors.New("USB AT is unavailable in this build")
}

func (u *usbAT) UploadFile(_ string, _ []byte, _ time.Duration) (string, error) {
	return "", errors.New("USB AT is unavailable in this build")
}

func (u *usbAT) DownloadFile(_ string, _ int, _ time.Duration) ([]byte, error) {
	return nil, errors.New("USB AT is unavailable in this build")
}
