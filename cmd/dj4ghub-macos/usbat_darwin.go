//go:build darwin && cgo

package main

/*
#cgo pkg-config: libusb-1.0
#include <stdlib.h>
#include <libusb.h>
*/
import "C"

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	djiUSBVendorID  = 0x2ca3
	djiUSBProductID = 0x4006
)

type usbAT struct {
	ctx         *C.libusb_context
	handle      *C.libusb_device_handle
	iface       int
	endpointIn  byte
	endpointOut byte
	mu          sync.Mutex
}

type usbATCandidate struct {
	iface       int
	endpointIn  byte
	endpointOut byte
}

func openDJIUSBAT() (*usbAT, error) {
	var ctx *C.libusb_context
	if rc := C.libusb_init(&ctx); rc != 0 {
		return nil, fmt.Errorf("libusb init: %s", usbErrorName(rc))
	}
	handle := C.libusb_open_device_with_vid_pid(ctx, djiUSBVendorID, djiUSBProductID)
	if handle == nil {
		C.libusb_exit(ctx)
		return nil, errors.New("DJI USB AT device 2ca3:4006 not found")
	}
	candidates, err := usbATCandidates(handle)
	if err != nil {
		C.libusb_close(handle)
		C.libusb_exit(ctx)
		return nil, err
	}
	var lastErr error
	for _, candidate := range candidates {
		if rc := C.libusb_claim_interface(handle, C.int(candidate.iface)); rc != 0 {
			lastErr = fmt.Errorf("claim USB AT interface %d: %s", candidate.iface, usbErrorName(rc))
			continue
		}
		dev := &usbAT{
			ctx:         ctx,
			handle:      handle,
			iface:       candidate.iface,
			endpointIn:  candidate.endpointIn,
			endpointOut: candidate.endpointOut,
		}
		if response, err := dev.Command("AT", 900*time.Millisecond); err == nil && atProbeSucceeded(response) {
			return dev, nil
		} else {
			if err == nil {
				err = fmt.Errorf("unexpected AT probe response %q", response)
			}
			lastErr = fmt.Errorf("probe USB AT interface %d out 0x%02x in 0x%02x: %w",
				candidate.iface, candidate.endpointOut, candidate.endpointIn, err)
		}
		C.libusb_release_interface(handle, C.int(candidate.iface))
	}
	C.libusb_close(handle)
	C.libusb_exit(ctx)
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no USB bulk interface candidates found for DJI AT bridge")
}

func usbATCandidates(handle *C.libusb_device_handle) ([]usbATCandidate, error) {
	dev := C.libusb_get_device(handle)
	if dev == nil {
		return nil, errors.New("libusb device handle has no device")
	}
	var config *C.struct_libusb_config_descriptor
	if rc := C.libusb_get_active_config_descriptor(dev, &config); rc != 0 {
		return nil, fmt.Errorf("get active USB config descriptor: %s", usbErrorName(rc))
	}
	defer C.libusb_free_config_descriptor(config)

	var candidates []usbATCandidate
	interfaces := unsafe.Slice(config._interface, int(config.bNumInterfaces))
	for _, intf := range interfaces {
		altsettings := unsafe.Slice(intf.altsetting, int(intf.num_altsetting))
		for _, alt := range altsettings {
			var endpointIn, endpointOut byte
			endpoints := unsafe.Slice(alt.endpoint, int(alt.bNumEndpoints))
			for _, ep := range endpoints {
				attrs := byte(ep.bmAttributes) & byte(C.LIBUSB_TRANSFER_TYPE_MASK)
				if attrs != byte(C.LIBUSB_TRANSFER_TYPE_BULK) {
					continue
				}
				addr := byte(ep.bEndpointAddress)
				if addr&byte(C.LIBUSB_ENDPOINT_IN) != 0 {
					endpointIn = addr
				} else {
					endpointOut = addr
				}
			}
			if endpointIn != 0 && endpointOut != 0 {
				candidates = append(candidates, usbATCandidate{
					iface:       int(alt.bInterfaceNumber),
					endpointIn:  endpointIn,
					endpointOut: endpointOut,
				})
			}
		}
	}
	return candidates, nil
}

func (u *usbAT) Close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return
	}
	C.libusb_release_interface(u.handle, C.int(u.iface))
	C.libusb_close(u.handle)
	C.libusb_exit(u.ctx)
	u.handle = nil
	u.ctx = nil
}

func (u *usbAT) Command(cmd string, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	payload := []byte(cmd + "\r")
	if err := u.bulkWriteLocked(u.endpointOut, payload, timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var chunks []string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return strings.Join(chunks, ""), err
		}
		if len(data) == 0 {
			continue
		}
		chunks = append(chunks, string(data))
		joined := strings.Join(chunks, "")
		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}
	if len(chunks) == 0 {
		return "", errors.New("USB AT command timed out without response")
	}
	return normalizeATResponse(strings.Join(chunks, "")), nil
}

// CommandWithPrompt executes an AT command that enters an interactive input
// state, then submits followUp after the modem returns its ">" prompt.
func (u *usbAT) CommandWithPrompt(cmd string, followUp []byte, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if len(followUp) == 0 {
		return "", errors.New("interactive AT follow-up is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var response strings.Builder
	promptReceived := false
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return normalizeATResponse(response.String()), err
		}
		if len(data) == 0 {
			continue
		}
		response.Write(data)
		joined := response.String()

		if !promptReceived {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			if !atResponseHasPrompt(joined) {
				continue
			}
			if err := u.bulkWriteLocked(u.endpointOut, followUp, time.Until(deadline)); err != nil {
				return normalizeATResponse(joined), err
			}
			promptReceived = true
			continue
		}

		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}

	if promptReceived {
		// ESC cancels a pending message editor on modems that still accept input.
		_ = u.bulkWriteLocked(u.endpointOut, []byte{0x1b}, 300*time.Millisecond)
	}
	if response.Len() == 0 {
		return "", errors.New("USB interactive AT command timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB interactive AT command timed out before completion")
}

// UploadFile writes one complete file to the module's UFS through QFUPL.
// QFUPL reports CONNECT before accepting the exact byte count declared in
// the command, unlike SMS submission which uses a '>' prompt and Ctrl-Z.
func (u *usbAT) UploadFile(filename string, data []byte, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	if filename == "" || strings.ContainsAny(filename, "\"\r\n") {
		return "", errors.New("invalid module filename")
	}
	if len(data) == 0 {
		return "", errors.New("module upload data is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	cmd := fmt.Sprintf("AT+QFUPL=\"%s\",%d,30,1", filename, len(data))
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var response strings.Builder
	connectOffset := -1
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		chunk, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return normalizeATResponse(response.String()), err
		}
		if len(chunk) == 0 {
			continue
		}
		response.Write(chunk)
		joined := response.String()
		if connectOffset < 0 {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			upper := strings.ToUpper(joined)
			idx := strings.Index(upper, "CONNECT")
			if idx < 0 {
				continue
			}
			connectOffset = idx + len("CONNECT")
			for offset := 0; offset < len(data); {
				end := offset + 16*1024
				if end > len(data) {
					end = len(data)
				}
				if err := u.bulkWriteLocked(u.endpointOut, data[offset:end], time.Until(deadline)); err != nil {
					return normalizeATResponse(joined), err
				}
				offset = end
			}
			continue
		}
		if connectOffset <= len(joined) && atResponseComplete(joined[connectOffset:]) {
			return normalizeATResponse(joined), nil
		}
	}
	if response.Len() == 0 {
		return "", errors.New("USB QFUPL timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB QFUPL timed out before completion")
}

// DownloadFile reads an exact-length UFS file through QFDWL. The caller first
// obtains its length with QFLST, which keeps arbitrary WAV bytes from being
// confused with a trailing AT response.
func (u *usbAT) DownloadFile(filename string, size int, timeout time.Duration) ([]byte, error) {
	if u == nil {
		return nil, errors.New("USB AT device is not open")
	}
	if filename == "" || strings.ContainsAny(filename, "\"\r\n") {
		return nil, errors.New("invalid module filename")
	}
	if size <= 0 {
		return nil, errors.New("module download size is invalid")
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return nil, errors.New("USB AT device is not open")
	}

	u.drainLocked()
	command := fmt.Sprintf("AT+QFDWL=\"%s\"", filename)
	if err := u.bulkWriteLocked(u.endpointOut, []byte(command+"\r"), timeout); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	var header strings.Builder
	data := make([]byte, 0, size)
	connected := false
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		chunk, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return nil, err
		}
		if len(chunk) == 0 {
			continue
		}
		if !connected {
			header.Write(chunk)
			joined := header.String()
			if atResponseIsError(joined) {
				return nil, errors.New(normalizeATResponse(joined))
			}
			idx := strings.Index(strings.ToUpper(joined), "CONNECT")
			if idx < 0 {
				continue
			}
			payload := []byte(joined[idx+len("CONNECT"):])
			payload = bytes.TrimLeft(payload, "\r\n")
			data = append(data, payload...)
			connected = true
		} else {
			data = append(data, chunk...)
		}
		if len(data) >= size {
			return append([]byte(nil), data[:size]...), nil
		}
	}
	if !connected {
		return nil, errors.New("USB QFDWL timed out before CONNECT")
	}
	return nil, fmt.Errorf("USB QFDWL short transfer: %d/%d", len(data), size)
}

var errUSBTimeout = errors.New("usb timeout")

func (u *usbAT) drainLocked() {
	for {
		if _, err := u.bulkReadLocked(u.endpointIn, 80*time.Millisecond); err != nil {
			return
		}
	}
}

func (u *usbAT) Description() string {
	if u == nil {
		return "USB AT"
	}
	return fmt.Sprintf("USB AT · 2ca3:4006 interface %d out 0x%02x in 0x%02x",
		u.iface, u.endpointOut, u.endpointIn)
}

func (u *usbAT) bulkWriteLocked(endpoint byte, payload []byte, timeout time.Duration) error {
	var transferred C.int
	ptr := unsafe.Pointer(&payload[0])
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(ptr),
		C.int(len(payload)),
		&transferred,
		C.uint(timeout.Milliseconds()),
	)
	if rc != 0 {
		return fmt.Errorf("USB bulk write: %s", usbErrorName(rc))
	}
	if int(transferred) != len(payload) {
		return fmt.Errorf("USB bulk write short transfer: %d/%d", int(transferred), len(payload))
	}
	return nil
}

func (u *usbAT) bulkReadLocked(endpoint byte, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 512)
	var transferred C.int
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)),
		&transferred,
		C.uint(timeout.Milliseconds()),
	)
	if rc == C.LIBUSB_ERROR_TIMEOUT {
		return nil, errUSBTimeout
	}
	if rc != 0 {
		return nil, fmt.Errorf("USB bulk read: %s", usbErrorName(rc))
	}
	return buf[:int(transferred)], nil
}

func usbErrorName(rc C.int) string {
	return C.GoString(C.libusb_error_name(rc))
}
