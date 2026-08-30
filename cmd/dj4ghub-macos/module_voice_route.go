package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const moduleVoiceRouteCommand = "/usr/local/bin/dj4ghub-module-voice-route"

// startModuleVoiceRoute starts the QDC507-side UAC/VoLTE bridge. It is a
// runtime-only operation: the helper loads no firmware and changes no modem
// configuration. The bridge is stopped when the active call ends.
func (a *app) startModuleVoiceRoute(parent context.Context) (bool, error) {
	if runtime.GOOS != "linux" || a.demo {
		return false, nil
	}
	a.moduleVoiceRouteMu.Lock()
	defer a.moduleVoiceRouteMu.Unlock()
	// The first call may need to upload the small runtime and wait for the
	// QDC507 ALSA devices to appear after its drivers load.
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, moduleVoiceRouteCommand, "start").CombinedOutput()
	if err == nil {
		return true, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return false, context.Canceled
	}
	detail := strings.TrimSpace(string(output))
	if len(detail) > 3000 {
		detail = detail[len(detail)-3000:]
	}
	if detail == "" {
		detail = err.Error()
	}
	return false, fmt.Errorf("模块语音桥启动失败: %s", detail)
}

func (a *app) stopModuleVoiceRoute() error {
	if runtime.GOOS != "linux" || a.demo {
		return nil
	}
	a.moduleVoiceRouteMu.Lock()
	defer a.moduleVoiceRouteMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, moduleVoiceRouteCommand, "stop").CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return errors.New(detail)
}
