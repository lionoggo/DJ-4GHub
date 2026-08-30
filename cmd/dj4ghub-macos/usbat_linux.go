//go:build linux && !cgo

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	djiUSBVendorID  = "2ca3"
	djiUSBProductID = "4006"

	// Linux USBDEVFS ioctls. Keeping this transport in Go means the QNAP
	// binary can be built without a Linux libusb toolchain or a base image.
	usbdevfsClaimInterface   = 0x8004550f
	usbdevfsReleaseInterface = 0x80045510
	usbdevfsBulk             = 0xc0185502
)

type usbAT struct {
	fd          int
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

// usbdevfsBulkTransfer is the 64-bit Linux ABI for USBDEVFS_BULK.
type usbdevfsBulkTransfer struct {
	Endpoint uint32
	Length   uint32
	Timeout  uint32
	_        uint32
	Data     uintptr
}

func openDJIUSBAT() (*usbAT, error) {
	path, candidates, err := findLinuxUSBAT()
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open DJI USB AT device %s: %w", path, err)
	}
	var lastErr error
	for _, candidate := range candidates {
		iface := uint32(candidate.iface)
		if _, err := usbATIoctl(fd, usbdevfsClaimInterface, unsafe.Pointer(&iface)); err != nil {
			lastErr = fmt.Errorf("claim USB AT interface %d: %w", candidate.iface, err)
			continue
		}
		dev := &usbAT{fd: fd, iface: candidate.iface, endpointIn: candidate.endpointIn, endpointOut: candidate.endpointOut}
		if response, err := dev.Command("AT", 900*time.Millisecond); err == nil && atProbeSucceeded(response) {
			return dev, nil
		} else {
			if err == nil {
				err = fmt.Errorf("unexpected AT probe response %q", response)
			}
			lastErr = fmt.Errorf("probe USB AT interface %d out 0x%02x in 0x%02x: %w", candidate.iface, candidate.endpointOut, candidate.endpointIn, err)
		}
		_, _ = usbATIoctl(fd, usbdevfsReleaseInterface, unsafe.Pointer(&iface))
	}
	_ = unix.Close(fd)
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no USB bulk interface candidates found for DJI AT bridge")
}

func findLinuxUSBAT() (string, []usbATCandidate, error) {
	entries, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return "", nil, fmt.Errorf("list Linux USB devices: %w", err)
	}
	for _, base := range entries {
		vendor, vendorErr := readUSBAttribute(base, "idVendor")
		product, productErr := readUSBAttribute(base, "idProduct")
		if vendorErr != nil || productErr != nil || vendor != djiUSBVendorID || product != djiUSBProductID {
			continue
		}
		bus, busErr := readUSBAttribute(base, "busnum")
		device, deviceErr := readUSBAttribute(base, "devnum")
		if busErr != nil || deviceErr != nil {
			return "", nil, fmt.Errorf("read DJI USB bus address: %w", errors.Join(busErr, deviceErr))
		}
		busNumber, busErr := strconv.Atoi(bus)
		deviceNumber, deviceErr := strconv.Atoi(device)
		if busErr != nil || deviceErr != nil {
			return "", nil, fmt.Errorf("parse DJI USB bus address %q/%q", bus, device)
		}
		descriptors, err := os.ReadFile(filepath.Join(base, "descriptors"))
		if err != nil {
			return "", nil, fmt.Errorf("read DJI USB descriptors: %w", err)
		}
		candidates := parseLinuxUSBATCandidates(descriptors)
		if len(candidates) == 0 {
			return "", nil, errors.New("no USB bulk interface candidates found for DJI AT bridge")
		}
		return fmt.Sprintf("/dev/bus/usb/%03d/%03d", busNumber, deviceNumber), candidates, nil
	}
	return "", nil, errors.New("DJI USB AT device 2ca3:4006 not found")
}

func readUSBAttribute(base, name string) (string, error) {
	value, err := os.ReadFile(filepath.Join(base, name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func parseLinuxUSBATCandidates(descriptors []byte) []usbATCandidate {
	var candidates []usbATCandidate
	current := usbATCandidate{iface: -1}
	flush := func() {
		if current.iface >= 0 && current.endpointIn != 0 && current.endpointOut != 0 {
			candidates = append(candidates, current)
		}
	}
	for offset := 0; offset+2 <= len(descriptors); {
		length := int(descriptors[offset])
		if length < 2 || offset+length > len(descriptors) {
			break
		}
		typ := descriptors[offset+1]
		switch typ {
		case 4: // USB_DT_INTERFACE
			flush()
			current = usbATCandidate{iface: int(descriptors[offset+2])}
		case 5: // USB_DT_ENDPOINT
			if current.iface >= 0 && length >= 4 && descriptors[offset+3]&0x03 == 0x02 {
				address := descriptors[offset+2]
				if address&0x80 != 0 {
					current.endpointIn = address
				} else {
					current.endpointOut = address
				}
			}
		}
		offset += length
	}
	flush()
	return candidates
}

func (u *usbAT) Close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.fd < 0 {
		return
	}
	iface := uint32(u.iface)
	_, _ = usbATIoctl(u.fd, usbdevfsReleaseInterface, unsafe.Pointer(&iface))
	_ = unix.Close(u.fd)
	u.fd = -1
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
	if u.fd < 0 {
		return "", errors.New("USB AT device is not open")
	}
	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	var chunks []string
	for time.Now().Before(deadline) {
		data, err := u.bulkReadLocked(u.endpointIn, boundedUSBTimeout(deadline))
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
	if u.fd < 0 {
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
		data, err := u.bulkReadLocked(u.endpointIn, boundedUSBTimeout(deadline))
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
		_ = u.bulkWriteLocked(u.endpointOut, []byte{0x1b}, 300*time.Millisecond)
	}
	if response.Len() == 0 {
		return "", errors.New("USB interactive AT command timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB interactive AT command timed out before completion")
}

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
	if u.fd < 0 {
		return "", errors.New("USB AT device is not open")
	}
	u.drainLocked()
	command := fmt.Sprintf("AT+QFUPL=\"%s\",%d,30,1", filename, len(data))
	if err := u.bulkWriteLocked(u.endpointOut, []byte(command+"\r"), timeout); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	var response strings.Builder
	connected := false
	for time.Now().Before(deadline) {
		chunk, err := u.bulkReadLocked(u.endpointIn, boundedUSBTimeout(deadline))
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
		if !connected {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			if !strings.Contains(strings.ToUpper(joined), "CONNECT") {
				continue
			}
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
			connected = true
			continue
		}
		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}
	if response.Len() == 0 {
		return "", errors.New("USB QFUPL timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB QFUPL timed out before completion")
}

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
	if u.fd < 0 {
		return nil, errors.New("USB AT device is not open")
	}
	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(fmt.Sprintf("AT+QFDWL=\"%s\"\r", filename)), timeout); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	var header strings.Builder
	data := make([]byte, 0, size)
	connected := false
	for time.Now().Before(deadline) {
		chunk, err := u.bulkReadLocked(u.endpointIn, boundedUSBTimeout(deadline))
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
			index := strings.Index(strings.ToUpper(joined), "CONNECT")
			if index < 0 {
				continue
			}
			payload := bytes.TrimLeft([]byte(joined[index+len("CONNECT"):]), "\r\n")
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
	return fmt.Sprintf("USB AT · 2ca3:4006 interface %d out 0x%02x in 0x%02x", u.iface, u.endpointOut, u.endpointIn)
}

func (u *usbAT) bulkWriteLocked(endpoint byte, payload []byte, timeout time.Duration) error {
	transferred, err := u.bulkTransferLocked(endpoint, payload, timeout)
	if err != nil {
		return fmt.Errorf("USB bulk write: %w", err)
	}
	if transferred != len(payload) {
		return fmt.Errorf("USB bulk write short transfer: %d/%d", transferred, len(payload))
	}
	return nil
}

func (u *usbAT) bulkReadLocked(endpoint byte, timeout time.Duration) ([]byte, error) {
	buffer := make([]byte, 512)
	transferred, err := u.bulkTransferLocked(endpoint, buffer, timeout)
	if err != nil {
		if errors.Is(err, unix.ETIMEDOUT) {
			return nil, errUSBTimeout
		}
		return nil, fmt.Errorf("USB bulk read: %w", err)
	}
	return buffer[:transferred], nil
}

func (u *usbAT) bulkTransferLocked(endpoint byte, data []byte, timeout time.Duration) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if timeout <= 0 {
		timeout = time.Millisecond
	}
	milliseconds := timeout.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	transfer := usbdevfsBulkTransfer{Endpoint: uint32(endpoint), Length: uint32(len(data)), Timeout: uint32(milliseconds), Data: uintptr(unsafe.Pointer(&data[0]))}
	transferred, err := usbATIoctl(u.fd, usbdevfsBulk, unsafe.Pointer(&transfer))
	if err != nil {
		return 0, err
	}
	return transferred, nil
}

func usbATIoctl(fd int, request uintptr, value unsafe.Pointer) (int, error) {
	result, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func boundedUSBTimeout(deadline time.Time) time.Duration {
	remaining := time.Until(deadline)
	if remaining > 900*time.Millisecond {
		return 900 * time.Millisecond
	}
	return remaining
}
