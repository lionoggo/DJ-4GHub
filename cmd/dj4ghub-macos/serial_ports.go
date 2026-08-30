package main

import "runtime"

func serialPortPatterns() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"/dev/cu.usbmodem*",
			"/dev/cu.usbserial*",
			"/dev/cu.wchusbserial*",
		}
	}
	return []string{
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
	}
}
