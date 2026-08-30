package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSMSForwardTemplate = "【DJ 4G Hub】\n发件人：{{sender}}\n时间：{{timestamp}}\n内容：{{content}}"
	maxAutomationRecipients   = 20
	maxAutomationDelay        = 120
	maxCallHistoryItems       = 40
	maxCallRecordingItems     = 60
	modulePromptFilename      = "dj4ghub_prompt.wav"
)

type automationConfig struct {
	SMS   smsForwardConfig     `json:"sms"`
	Calls callAutomationConfig `json:"calls"`
}

type smsForwardConfig struct {
	Enabled          bool                  `json:"enabled"`
	RecipientNumbers []string              `json:"recipient_numbers"`
	SenderAllowlist  []string              `json:"sender_allowlist"`
	TextTemplate     string                `json:"text_template"`
	Telegram         telegramForwardConfig `json:"telegram"`
	Feishu           feishuForwardConfig   `json:"feishu"`
}

type telegramForwardConfig struct {
	Enabled    bool     `json:"enabled"`
	BotToken   string   `json:"bot_token,omitempty"`
	ChatIDs    []string `json:"chat_ids"`
	ClearToken bool     `json:"clear_token,omitempty"`
}

type feishuForwardConfig struct {
	Enabled       bool   `json:"enabled"`
	WebhookURL    string `json:"webhook_url"`
	SigningSecret string `json:"signing_secret,omitempty"`
	ClearSecret   bool   `json:"clear_secret,omitempty"`
}

type callAutomationConfig struct {
	Enabled                     bool     `json:"enabled"`
	AllowedNumbers              []string `json:"allowed_numbers"`
	AnswerAfterSeconds          int      `json:"answer_after_seconds"`
	HangupAfterSeconds          int      `json:"hangup_after_seconds"`
	PromptText                  string   `json:"prompt_text"`
	PromptFile                  string   `json:"prompt_file"`
	PlaybackCommand             string   `json:"playback_command"`
	EnableUSBAudio              bool     `json:"enable_usb_audio"`
	RecordCalls                 bool     `json:"record_calls"`
	ForwardRecordingsToTelegram bool     `json:"forward_recordings_to_telegram"`
	RecordingDirectory          string   `json:"recording_directory"`
	RecordingNoticeOK           bool     `json:"recording_notice_confirmed"`
}

type automationView struct {
	SMS   smsForwardView     `json:"sms"`
	Calls callAutomationView `json:"calls"`
}

type smsForwardView struct {
	Enabled          bool                `json:"enabled"`
	RecipientNumbers []string            `json:"recipient_numbers"`
	SenderAllowlist  []string            `json:"sender_allowlist"`
	TextTemplate     string              `json:"text_template"`
	Telegram         telegramForwardView `json:"telegram"`
	Feishu           feishuForwardView   `json:"feishu"`
}

type telegramForwardView struct {
	Enabled     bool     `json:"enabled"`
	BotTokenSet bool     `json:"bot_token_set"`
	ChatIDs     []string `json:"chat_ids"`
}

type feishuForwardView struct {
	Enabled          bool   `json:"enabled"`
	WebhookURL       string `json:"webhook_url"`
	SigningSecretSet bool   `json:"signing_secret_set"`
}

type callAutomationView struct {
	Enabled                     bool     `json:"enabled"`
	AllowedNumbers              []string `json:"allowed_numbers"`
	AnswerAfterSeconds          int      `json:"answer_after_seconds"`
	HangupAfterSeconds          int      `json:"hangup_after_seconds"`
	PromptText                  string   `json:"prompt_text"`
	PromptFile                  string   `json:"prompt_file"`
	PlaybackCommand             string   `json:"playback_command"`
	EnableUSBAudio              bool     `json:"enable_usb_audio"`
	RecordCalls                 bool     `json:"record_calls"`
	ForwardRecordingsToTelegram bool     `json:"forward_recordings_to_telegram"`
	RecordingDirectory          string   `json:"recording_directory"`
	RecordingNoticeOK           bool     `json:"recording_notice_confirmed"`
}

type callRuntimeStatus struct {
	State     string    `json:"state"`
	Number    string    `json:"number,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Answered  bool      `json:"answered"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type callHistoryItem struct {
	ID                  uint64    `json:"id"`
	Number              string    `json:"number,omitempty"`
	State               string    `json:"state"`
	Detail              string    `json:"detail,omitempty"`
	Answered            bool      `json:"answered"`
	StartedAt           time.Time `json:"started_at"`
	EndedAt             time.Time `json:"ended_at,omitempty"`
	RecordingName       string    `json:"recording_name,omitempty"`
	ForwardedToTelegram bool      `json:"forwarded_to_telegram"`
}

type callRecordingView struct {
	Name                string    `json:"name"`
	Number              string    `json:"number,omitempty"`
	RecordedAt          time.Time `json:"recorded_at"`
	Size                int64     `json:"size"`
	DownloadURL         string    `json:"download_url"`
	ForwardedToTelegram bool      `json:"forwarded_to_telegram"`
}

type callsView struct {
	Active     bool                `json:"active"`
	Call       callRuntimeStatus   `json:"call"`
	History    []callHistoryItem   `json:"history"`
	Recordings []callRecordingView `json:"recordings"`
}

type activeCall struct {
	ID              uint64
	Number          string
	ctx             context.Context
	cancel          context.CancelFunc
	workflowClaimed bool
	recording       *callRecording
}

type callRecording struct {
	callID         uint64
	moduleFilename string
	directory      string
	startedAt      time.Time
	rawPath        string
	outputPath     string
	cancel         context.CancelFunc
	command        *exec.Cmd
}

func defaultAutomationConfig() automationConfig {
	return automationConfig{
		SMS: smsForwardConfig{TextTemplate: defaultSMSForwardTemplate},
		Calls: callAutomationConfig{
			AnswerAfterSeconds: 2,
			HangupAfterSeconds: 12,
		},
	}
}

func (a *app) loadAutomationLocked() error {
	if a.automationLoaded {
		return nil
	}
	path := strings.TrimSpace(a.automationPath)
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("locate automation config directory: %w", err)
		}
		path = filepath.Join(configDir, "DJ 4G Hub", "automation.json")
		a.automationPath = path
	}

	config := defaultAutomationConfig()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read automation config: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse automation config: %w", err)
		}
	}
	if err := normalizeAutomationConfig(&config); err != nil {
		return err
	}
	a.automation = config
	a.automationLoaded = true
	return nil
}

func (a *app) automationSnapshot() (automationConfig, error) {
	a.automationMu.Lock()
	defer a.automationMu.Unlock()
	if err := a.loadAutomationLocked(); err != nil {
		return automationConfig{}, err
	}
	return cloneAutomationConfig(a.automation), nil
}

func cloneAutomationConfig(value automationConfig) automationConfig {
	value.SMS.RecipientNumbers = append([]string(nil), value.SMS.RecipientNumbers...)
	value.SMS.SenderAllowlist = append([]string(nil), value.SMS.SenderAllowlist...)
	value.SMS.Telegram.ChatIDs = append([]string(nil), value.SMS.Telegram.ChatIDs...)
	value.Calls.AllowedNumbers = append([]string(nil), value.Calls.AllowedNumbers...)
	return value
}

func normalizeAutomationConfig(config *automationConfig) error {
	if config == nil {
		return errors.New("automation config is required")
	}
	config.SMS.RecipientNumbers = normalizePhoneList(config.SMS.RecipientNumbers, "短信转发手机号")
	config.SMS.SenderAllowlist = normalizePhoneList(config.SMS.SenderAllowlist, "短信发送人白名单")
	config.SMS.Telegram.ChatIDs = normalizeNonEmptyList(config.SMS.Telegram.ChatIDs, maxAutomationRecipients, "Telegram Chat ID")
	config.Calls.AllowedNumbers = normalizePhoneList(config.Calls.AllowedNumbers, "来电白名单")
	config.SMS.TextTemplate = strings.TrimSpace(config.SMS.TextTemplate)
	if config.SMS.TextTemplate == "" {
		config.SMS.TextTemplate = defaultSMSForwardTemplate
	}
	if len(config.SMS.TextTemplate) > 1200 {
		return errors.New("短信转发模板不能超过 1200 个字符")
	}
	config.SMS.Telegram.BotToken = strings.TrimSpace(config.SMS.Telegram.BotToken)
	if (config.SMS.Telegram.Enabled || config.Calls.ForwardRecordingsToTelegram) && (config.SMS.Telegram.BotToken == "" || len(config.SMS.Telegram.ChatIDs) == 0) {
		return errors.New("启用 Telegram 转发时必须填写 Bot Token 和至少一个 Chat ID")
	}
	config.SMS.Feishu.WebhookURL = strings.TrimSpace(config.SMS.Feishu.WebhookURL)
	config.SMS.Feishu.SigningSecret = strings.TrimSpace(config.SMS.Feishu.SigningSecret)
	if config.SMS.Feishu.Enabled {
		parsed, err := url.Parse(config.SMS.Feishu.WebhookURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("启用飞书转发时必须填写有效的 HTTPS Webhook 地址")
		}
	}
	if config.Calls.AnswerAfterSeconds < 0 || config.Calls.AnswerAfterSeconds > maxAutomationDelay {
		return fmt.Errorf("接听延迟必须在 0 到 %d 秒之间", maxAutomationDelay)
	}
	if config.Calls.HangupAfterSeconds < 0 || config.Calls.HangupAfterSeconds > maxAutomationDelay {
		return fmt.Errorf("自动挂断时间必须在 0 到 %d 秒之间", maxAutomationDelay)
	}
	config.Calls.PromptFile = strings.TrimSpace(config.Calls.PromptFile)
	config.Calls.PromptText = strings.TrimSpace(config.Calls.PromptText)
	config.Calls.PlaybackCommand = strings.TrimSpace(config.Calls.PlaybackCommand)
	config.Calls.RecordingDirectory = strings.TrimSpace(config.Calls.RecordingDirectory)
	if len(config.Calls.PromptFile) > 0 && !filepath.IsAbs(config.Calls.PromptFile) {
		return errors.New("提示音文件必须使用绝对路径")
	}
	if len([]rune(config.Calls.PromptText)) > 300 {
		return errors.New("提示语文本不能超过 300 个字符")
	}
	if len(config.Calls.PlaybackCommand) > 1200 {
		return errors.New("提示音播放命令不能超过 1200 个字符")
	}
	if config.Calls.PlaybackCommand != "" && !strings.Contains(config.Calls.PlaybackCommand, "{{file}}") {
		return errors.New("提示音播放命令必须包含 {{file}} 占位符")
	}
	if config.Calls.RecordingDirectory != "" && !filepath.IsAbs(config.Calls.RecordingDirectory) {
		return errors.New("录音保存目录必须使用绝对路径")
	}
	if config.Calls.RecordCalls && !config.Calls.RecordingNoticeOK {
		return errors.New("启用录音前，须确认录音用途及保存方式符合适用规则")
	}
	if config.Calls.ForwardRecordingsToTelegram && !config.Calls.RecordCalls {
		return errors.New("转发通话录音前，须先启用录制来电方语音")
	}
	return nil
}

func normalizePhoneList(values []string, label string) []string {
	values = normalizeNonEmptyList(values, maxAutomationRecipients, label)
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizePhone(value)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, normalized)
	}
	return result
}

func normalizeNonEmptyList(values []string, limit int, label string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ';' }) {
			value := strings.TrimSpace(part)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			result = append(result, value)
			if len(result) >= limit {
				return result
			}
		}
	}
	sort.Strings(result)
	return result
}

func normalizePhone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var result strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			result.WriteRune(r)
		case r == '+' && result.Len() == 0:
			result.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')':
			continue
		default:
			return strings.ToUpper(value)
		}
	}
	normalized := result.String()
	digits := strings.TrimPrefix(normalized, "+")
	if len(digits) < 3 || len(digits) > 20 {
		return ""
	}
	return normalized
}

func (a *app) persistAutomationLocked() error {
	if err := os.MkdirAll(filepath.Dir(a.automationPath), 0o700); err != nil {
		return fmt.Errorf("create automation config directory: %w", err)
	}
	config := cloneAutomationConfig(a.automation)
	config.SMS.Telegram.ClearToken = false
	config.SMS.Feishu.ClearSecret = false
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode automation config: %w", err)
	}
	temporary := a.automationPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write automation config: %w", err)
	}
	if err := os.Rename(temporary, a.automationPath); err != nil {
		return fmt.Errorf("replace automation config: %w", err)
	}
	return nil
}

func automationConfigView(config automationConfig) automationView {
	return automationView{
		SMS: smsForwardView{
			Enabled:          config.SMS.Enabled,
			RecipientNumbers: append([]string(nil), config.SMS.RecipientNumbers...),
			SenderAllowlist:  append([]string(nil), config.SMS.SenderAllowlist...),
			TextTemplate:     config.SMS.TextTemplate,
			Telegram: telegramForwardView{
				Enabled:     config.SMS.Telegram.Enabled,
				BotTokenSet: config.SMS.Telegram.BotToken != "",
				ChatIDs:     append([]string(nil), config.SMS.Telegram.ChatIDs...),
			},
			Feishu: feishuForwardView{
				Enabled:          config.SMS.Feishu.Enabled,
				WebhookURL:       config.SMS.Feishu.WebhookURL,
				SigningSecretSet: config.SMS.Feishu.SigningSecret != "",
			},
		},
		Calls: callAutomationView{
			Enabled:                     config.Calls.Enabled,
			AllowedNumbers:              append([]string(nil), config.Calls.AllowedNumbers...),
			AnswerAfterSeconds:          config.Calls.AnswerAfterSeconds,
			HangupAfterSeconds:          config.Calls.HangupAfterSeconds,
			PromptText:                  config.Calls.PromptText,
			PromptFile:                  config.Calls.PromptFile,
			PlaybackCommand:             config.Calls.PlaybackCommand,
			EnableUSBAudio:              config.Calls.EnableUSBAudio,
			RecordCalls:                 config.Calls.RecordCalls,
			ForwardRecordingsToTelegram: config.Calls.ForwardRecordingsToTelegram,
			RecordingDirectory:          config.Calls.RecordingDirectory,
			RecordingNoticeOK:           config.Calls.RecordingNoticeOK,
		},
	}
}

func (a *app) getAutomation(w http.ResponseWriter, _ *http.Request) {
	config, err := a.automationSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, automationConfigView(config))
}

func (a *app) updateAutomation(w http.ResponseWriter, r *http.Request) {
	var incoming automationConfig
	if !decodeJSON(w, r, &incoming) {
		return
	}
	a.automationMu.Lock()
	defer a.automationMu.Unlock()
	if err := a.loadAutomationLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	previous := a.automation
	if incoming.SMS.Telegram.BotToken == "" && !incoming.SMS.Telegram.ClearToken {
		incoming.SMS.Telegram.BotToken = previous.SMS.Telegram.BotToken
	}
	if incoming.SMS.Feishu.SigningSecret == "" && !incoming.SMS.Feishu.ClearSecret {
		incoming.SMS.Feishu.SigningSecret = previous.SMS.Feishu.SigningSecret
	}
	if incoming.SMS.Telegram.ClearToken {
		incoming.SMS.Telegram.BotToken = ""
	}
	if incoming.SMS.Feishu.ClearSecret {
		incoming.SMS.Feishu.SigningSecret = ""
	}
	if incoming.Calls.PromptText != "" {
		promptFile, err := a.generateTypedPrompt(incoming.Calls.PromptText)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		incoming.Calls.PromptFile = promptFile
	}
	if err := normalizeAutomationConfig(&incoming); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.automation = incoming
	if err := a.persistAutomationLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Keep a module-native copy ready even when a host playback command is
	// configured. QPSND sends the prompt directly into the live cellular call;
	// host USB audio is only a fallback for devices that do not support it.
	if incoming.Calls.Enabled && incoming.Calls.PromptFile != "" && a.modem == nil && !a.demo {
		calls := incoming.Calls
		go func() {
			if err := a.ensureModulePrompt(calls.PromptFile); err != nil {
				log.Printf("module prompt preparation after configuration update failed: %v", err)
			}
		}()
	}
	writeJSON(w, http.StatusOK, automationConfigView(incoming))
}

func (a *app) generateTypedPrompt(text string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("当前系统不能直接生成提示音，请改用提示音文件")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("提示语文本不能为空")
	}
	configPath := strings.TrimSpace(a.automationPath)
	if configPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate prompt directory: %w", err)
		}
		configPath = filepath.Join(configDir, "DJ 4G Hub", "automation.json")
	}
	directory := filepath.Join(filepath.Dir(configPath), "prompts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create prompt directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "typed-prompt-*.aiff")
	if err != nil {
		return "", fmt.Errorf("create typed prompt: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return "", err
	}
	defer os.Remove(temporaryPath)
	output, err := exec.Command("say", "-v", "Tingting", "-o", temporaryPath, text).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("generate prompt speech: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	destination := filepath.Join(directory, "typed-prompt.aiff")
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", fmt.Errorf("save typed prompt: %w", err)
	}
	return destination, nil
}

func (a *app) getAutomationStatus(w http.ResponseWriter, _ *http.Request) {
	a.callMu.Lock()
	status := a.callStatus
	a.callMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"call": status})
}

func (a *app) getCalls(w http.ResponseWriter, _ *http.Request) {
	config, err := a.automationSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.callMu.Lock()
	active := a.activeCall != nil
	status := a.callStatus
	history := make([]callHistoryItem, len(a.callHistory))
	for index := range a.callHistory {
		history[len(a.callHistory)-1-index] = a.callHistory[index]
	}
	a.callMu.Unlock()
	recordings, err := a.callRecordingViews(config.Calls, history)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, callsView{Active: active, Call: status, History: history, Recordings: recordings})
}

func (a *app) answerActiveCall(w http.ResponseWriter, _ *http.Request) {
	config, err := a.automationSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.callMu.Lock()
	if a.activeCall == nil {
		a.callMu.Unlock()
		writeError(w, http.StatusConflict, "当前没有等待接听的来电")
		return
	}
	id := a.activeCall.ID
	ctx := a.activeCall.ctx
	number := a.activeCall.Number
	a.callMu.Unlock()
	if !a.claimCallWorkflow(id) {
		writeError(w, http.StatusConflict, "该来电已由自动流程处理")
		return
	}
	a.setCallStatus(id, "正在手动接听", "已从通话控制台发出接听指令", false)
	modulePromptReady := a.prepareModulePrompt(config.Calls)
	if err := a.answerCall(); err != nil {
		a.releaseCallWorkflow(id)
		a.setCallStatus(id, "接听失败", err.Error(), false)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	log.Printf("incoming call manually answered from %s", displayCallNumber(number))
	go a.runAnsweredCallWorkflow(id, ctx, config, modulePromptReady)
	writeJSON(w, http.StatusOK, map[string]string{"message": "已接听，正在执行提示音与录音流程"})
}

func (a *app) hangupActiveCall(w http.ResponseWriter, _ *http.Request) {
	a.callMu.Lock()
	active := a.activeCall != nil
	a.callMu.Unlock()
	if !active {
		writeError(w, http.StatusConflict, "当前没有进行中的来电")
		return
	}
	if err := a.hangupCall(); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.clearActiveCall("已手动挂断")
	writeJSON(w, http.StatusOK, map[string]string{"message": "通话已挂断"})
}

func (a *app) callRecordingViews(config callAutomationConfig, history []callHistoryItem) ([]callRecordingView, error) {
	directory := a.callRecordingDirectory(config)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []callRecordingView{}, nil
	}
	if err != nil {
		return nil, err
	}
	historyByRecording := make(map[string]callHistoryItem, len(history))
	for _, item := range history {
		if item.RecordingName != "" {
			historyByRecording[item.RecordingName] = item
		}
	}
	result := make([]callRecordingView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isCallRecordingName(entry.Name()) {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Size() <= 44 {
			continue
		}
		historyItem := historyByRecording[entry.Name()]
		result = append(result, callRecordingView{
			Name:                entry.Name(),
			Number:              historyItem.Number,
			RecordedAt:          info.ModTime(),
			Size:                info.Size(),
			DownloadURL:         "/api/calls/recordings/" + url.PathEscape(entry.Name()),
			ForwardedToTelegram: historyItem.ForwardedToTelegram,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RecordedAt.After(result[j].RecordedAt) })
	if len(result) > maxCallRecordingItems {
		result = result[:maxCallRecordingItems]
	}
	return result, nil
}

func (a *app) downloadCallRecording(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isCallRecordingName(name) {
		writeError(w, http.StatusNotFound, "未找到录音文件")
		return
	}
	config, err := a.automationSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := filepath.Join(a.callRecordingDirectory(config.Calls), name)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "未找到录音文件")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "未找到录音文件")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", "inline; filename=\""+name+"\"")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func isCallRecordingName(name string) bool {
	return filepath.Base(name) == name && strings.HasPrefix(name, "dj4ghub_call_") && strings.HasSuffix(name, ".wav")
}

func (a *app) recordIncomingSMS(sender, content string, timestamp time.Time) {
	items, _ := a.mergeSMS([]receivedSMS{{Sender: sender, Content: content, Timestamp: timestamp}})
	for _, item := range items {
		go a.forwardIncomingSMS(item)
	}
}

func (a *app) forwardIncomingSMS(item receivedSMS) {
	config, err := a.automationSnapshot()
	if err != nil {
		log.Printf("SMS forwarding configuration unavailable: %v", err)
		return
	}
	if !config.SMS.Enabled || !numberAllowed(item.Sender, config.SMS.SenderAllowlist) {
		return
	}
	text := renderSMSForwardText(config.SMS.TextTemplate, item)
	for _, number := range config.SMS.RecipientNumbers {
		if _, err := a.sendTextSMS(number, text); err != nil {
			log.Printf("SMS forwarding to %s failed: %v", number, err)
		} else {
			log.Printf("SMS forwarded to %s", number)
		}
	}
	if config.SMS.Telegram.Enabled {
		for _, chatID := range config.SMS.Telegram.ChatIDs {
			if err := sendTelegramMessage(context.Background(), config.SMS.Telegram.BotToken, chatID, text); err != nil {
				log.Printf("Telegram SMS forwarding to %s failed: %v", chatID, err)
			}
		}
	}
	if config.SMS.Feishu.Enabled {
		if err := sendFeishuMessage(context.Background(), config.SMS.Feishu, text); err != nil {
			log.Printf("Feishu SMS forwarding failed: %v", err)
		}
	}
}

func renderSMSForwardText(template string, item receivedSMS) string {
	if strings.TrimSpace(template) == "" {
		template = defaultSMSForwardTemplate
	}
	replacements := strings.NewReplacer(
		"{{sender}}", item.Sender,
		"{{content}}", item.Content,
		"{{timestamp}}", item.Timestamp.Local().Format("2006-01-02 15:04:05"),
		"{{code}}", item.Code,
	)
	return replacements.Replace(template)
}

func numberAllowed(number string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	normalized := normalizePhone(number)
	for _, allowed := range allowlist {
		if strings.EqualFold(normalized, allowed) {
			return true
		}
	}
	return false
}

func sendTelegramMessage(ctx context.Context, token, chatID, text string) error {
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(token) + "/sendMessage"
	payload := map[string]string{"chat_id": chatID, "text": text}
	return postJSONWithRetry(ctx, endpoint, payload, nil)
}

// sendTelegramDocument delivers a completed local recording as a Telegram
// attachment. WAV is deliberately kept as-is: it avoids a second lossy audio
// conversion and remains playable or downloadable from the chat.
func sendTelegramDocument(ctx context.Context, token, chatID, path, caption string) error {
	endpoint := "https://api.telegram.org/bot" + url.PathEscape(token) + "/sendDocument"
	return sendTelegramDocumentToEndpoint(ctx, endpoint, chatID, path, caption)
}

func sendTelegramDocumentToEndpoint(ctx context.Context, endpoint, chatID, path, caption string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read recording: %w", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if strings.TrimSpace(caption) != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return postMultipartWithRetry(ctx, endpoint, body.Bytes(), writer.FormDataContentType())
}

func sendFeishuMessage(ctx context.Context, config feishuForwardConfig, text string) error {
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if config.SigningSecret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		source := timestamp + "\n" + config.SigningSecret
		hash := hmac.New(sha256.New, []byte(source))
		payload["timestamp"] = timestamp
		payload["sign"] = base64.StdEncoding.EncodeToString(hash.Sum(nil))
	}
	return postJSONWithRetry(ctx, config.WebhookURL, payload, nil)
}

func postJSONWithRetry(ctx context.Context, endpoint string, payload any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			for key, value := range headers {
				req.Header.Set(key, value)
			}
			resp, doErr := http.DefaultClient.Do(req)
			if doErr == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					cancel()
					return nil
				}
				doErr = fmt.Errorf("HTTP %s", resp.Status)
			}
			lastErr = doErr
		} else {
			lastErr = err
		}
		cancel()
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return lastErr
}

func postMultipartWithRetry(ctx context.Context, endpoint string, body []byte, contentType string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", contentType)
			resp, doErr := http.DefaultClient.Do(req)
			if doErr == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					cancel()
					return nil
				}
				doErr = fmt.Errorf("HTTP %s", resp.Status)
			}
			lastErr = doErr
		} else {
			lastErr = err
		}
		cancel()
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return lastErr
}

func (a *app) observeIncomingCall(number string) {
	config, err := a.automationSnapshot()
	if err != nil || !config.Calls.Enabled {
		return
	}
	number = normalizePhone(number)
	a.callMu.Lock()
	if a.activeCall != nil {
		if number != "" {
			a.activeCall.Number = number
			a.callStatus.Number = number
			a.callStatus.UpdatedAt = time.Now()
			a.updateCallHistoryLocked(a.activeCall.ID, func(item *callHistoryItem) { item.Number = number })
		}
		a.callMu.Unlock()
		return
	}
	a.callEventID++
	ctx, cancel := context.WithCancel(context.Background())
	call := &activeCall{ID: a.callEventID, Number: number, ctx: ctx, cancel: cancel}
	a.activeCall = call
	now := time.Now()
	a.callStatus = callRuntimeStatus{State: "检测到来电", Number: number, Detail: "等待自动化规则", UpdatedAt: now}
	a.callHistory = append(a.callHistory, callHistoryItem{ID: call.ID, Number: number, State: "检测到来电", Detail: "等待自动化规则", StartedAt: now})
	if len(a.callHistory) > maxCallHistoryItems {
		a.callHistory = append([]callHistoryItem(nil), a.callHistory[len(a.callHistory)-maxCallHistoryItems:]...)
	}
	a.callMu.Unlock()
	log.Printf("incoming call detected from %s", displayCallNumber(number))
	go a.processIncomingCall(call.ID, ctx)
}

func (a *app) processIncomingCall(id uint64, ctx context.Context) {
	config, err := a.automationSnapshot()
	if err != nil {
		a.setCallStatus(id, "配置读取失败", "", false)
		return
	}
	if !waitForCall(ctx, time.Duration(config.Calls.AnswerAfterSeconds)*time.Second) {
		return
	}
	number := a.currentCallNumber(id)
	if !numberAllowed(number, config.Calls.AllowedNumbers) {
		a.setCallStatus(id, "来电未接听", "号码不在白名单中", false)
		return
	}
	modulePromptReady := a.prepareModulePrompt(config.Calls)
	if !a.claimCallWorkflow(id) {
		return
	}
	log.Printf("answering incoming call from %s", displayCallNumber(number))
	if err := a.answerCall(); err != nil {
		a.releaseCallWorkflow(id)
		a.setCallStatus(id, "接听失败", err.Error(), false)
		log.Printf("incoming call answer failed: %v", err)
		return
	}
	log.Printf("incoming call answered")
	a.runAnsweredCallWorkflow(id, ctx, config, modulePromptReady)
}

func (a *app) runAnsweredCallWorkflow(id uint64, ctx context.Context, config automationConfig, modulePromptReady bool) {
	number := a.currentCallNumber(id)
	// This module rejects QPCMV while a call is still ringing.  Switch the
	// audio route only after ATA has put the call into its connected state.
	if config.Calls.EnableUSBAudio {
		if !waitForCall(ctx, 300*time.Millisecond) {
			return
		}
		if response, err := a.runATCommand("AT+QPCMV=1,2", 3*time.Second); err != nil || !atCommandSucceeded(response) {
			log.Printf("USB voice audio preparation failed after answer: response=%q err=%v", response, err)
		}
	}
	a.setCallStatus(id, "已接听", "正在播放提示音", true)
	if config.Calls.RecordCalls {
		// QDC507's QAUDRD command is rejected while its VoLTE bridge is active.
		// Begin recording from the verified UAC downlink after the prompt has
		// finished, so it cannot contend with the caller-facing prompt stream.
		go a.scheduleHostCallRecording(id, ctx, config.Calls)
	}
	if config.Calls.PromptFile != "" {
		if !waitForCall(ctx, 500*time.Millisecond) {
			return
		}
		playedInModule := false
		if modulePromptReady {
			log.Printf("starting module-native call prompt playback")
			if command, response, err := a.startModulePromptPlayback(); err == nil {
				playedInModule = true
				log.Printf("module-native call prompt playback started with %s: %q", command, response)
			} else {
				log.Printf("module-native call prompt playback failed: %v", err)
			}
		}
		if !playedInModule {
			log.Printf("starting host call prompt playback fallback")
			if err := playPrompt(ctx, config.Calls, number); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("call prompt playback failed: %v", err)
				a.setCallStatus(id, "已接听", "提示音播放失败："+err.Error(), true)
			} else if err == nil {
				log.Printf("host call prompt playback completed")
			}
		}
	}
	if config.Calls.HangupAfterSeconds <= 0 {
		return
	}
	if !waitForCall(ctx, time.Duration(config.Calls.HangupAfterSeconds)*time.Second) {
		return
	}
	if err := a.hangupCall(); err != nil {
		a.setCallStatus(id, "自动挂断失败", err.Error(), true)
		log.Printf("automatic call hangup failed: %v", err)
		return
	}
	log.Printf("incoming call hung up automatically")
	a.clearActiveCall("已自动挂断")
}

func (a *app) prepareModulePrompt(config callAutomationConfig) bool {
	// QPSND sends a UFS WAV file directly to the far end of an active call.
	// USB audio remains the fallback for module firmware that rejects QPSND.
	if config.PromptFile == "" || a.modem != nil || a.demo {
		return false
	}
	if err := a.ensureModulePrompt(config.PromptFile); err != nil {
		log.Printf("module prompt preparation failed; host playback remains available: %v", err)
		return false
	}
	return true
}

func (a *app) startModulePromptPlayback() (string, string, error) {
	commands := modulePromptPlaybackCommands()
	var attempts []string
	for _, command := range commands {
		response, err := a.runATCommand(command, 3*time.Second)
		if err == nil && atCommandSucceeded(response) {
			return command, response, nil
		}
		attempts = append(attempts, fmt.Sprintf("%s => %q (%v)", command, response, err))
	}
	return "", "", errors.New(strings.Join(attempts, "; "))
}

func modulePromptPlaybackCommands() []string {
	// AT+QPSND's final two arguments preserve the normal uplink and downlink
	// audio paths while the WAV is injected to the far end. Omitting them, or
	// using 0, is not a caller-facing prompt route on this modem firmware.
	return []string{
		fmt.Sprintf("AT+QPSND=1,\"%s\",0,1,1", modulePromptFilename),
		fmt.Sprintf("AT+QPSND=1,\"UFS:%s\",0,1,1", modulePromptFilename),
	}
}

func (a *app) ensureModulePrompt(source string) error {
	stat, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("提示音文件不可用: %w", err)
	}

	a.modulePromptMu.Lock()
	defer a.modulePromptMu.Unlock()
	if a.modulePromptSource == source && a.modulePromptModTime == stat.ModTime().UnixNano() {
		return nil
	}
	if err := a.ensureUSBAT(); err != nil {
		return err
	}
	if a.usbAT == nil {
		return errors.New("USB AT device is unavailable for module prompt upload")
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil && runtime.GOOS == "darwin" {
		if _, statErr := os.Stat("/opt/homebrew/bin/ffmpeg"); statErr == nil {
			ffmpegPath = "/opt/homebrew/bin/ffmpeg"
			err = nil
		}
	}
	if err != nil {
		return errors.New("ffmpeg is required to prepare the module prompt")
	}
	temporary, err := os.CreateTemp("", "dj4ghub-module-prompt-*.wav")
	if err != nil {
		return fmt.Errorf("create temporary module prompt: %w", err)
	}
	temporaryPath := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		return closeErr
	}
	defer os.Remove(temporaryPath)

	command := exec.Command(
		ffmpegPath, "-nostdin", "-v", "error", "-y", "-i", source,
		"-ar", "8000", "-ac", "1", "-c:a", "pcm_s16le", temporaryPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("convert module prompt: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	data, err := os.ReadFile(temporaryPath)
	if err != nil {
		return fmt.Errorf("read converted module prompt: %w", err)
	}
	// QDC507 exposes QFUPL's optional overwrite parameter but still rejects an
	// existing filename. Delete the previous UFS copy explicitly before upload.
	deleteResponse, deleteErr := a.runATCommand(
		fmt.Sprintf("AT+QFDEL=\"%s\"", modulePromptFilename),
		3*time.Second,
	)
	if deleteErr != nil {
		return fmt.Errorf("delete previous module prompt: %w", deleteErr)
	}
	if !atCommandSucceeded(deleteResponse) && !strings.Contains(strings.ToLower(deleteResponse), "not found") {
		log.Printf("module prompt delete returned %q; continuing with upload", deleteResponse)
	}
	response, err := a.usbAT.UploadFile(modulePromptFilename, data, 30*time.Second)
	if err != nil {
		return err
	}
	if !atCommandSucceeded(response) {
		return fmt.Errorf("QFUPL: %s", strings.TrimSpace(response))
	}
	a.modulePromptSource = source
	a.modulePromptModTime = stat.ModTime().UnixNano()
	log.Printf("module prompt uploaded to UFS as %s (%d bytes)", modulePromptFilename, len(data))
	return nil
}

func waitForCall(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *app) currentCallNumber(id uint64) string {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall == nil || a.activeCall.ID != id {
		return ""
	}
	return a.activeCall.Number
}

func (a *app) claimCallWorkflow(id uint64) bool {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall == nil || a.activeCall.ID != id || a.activeCall.workflowClaimed {
		return false
	}
	a.activeCall.workflowClaimed = true
	return true
}

func (a *app) releaseCallWorkflow(id uint64) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall != nil && a.activeCall.ID == id {
		a.activeCall.workflowClaimed = false
	}
}

func (a *app) setCallStatus(id uint64, state, detail string, answered bool) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall == nil || a.activeCall.ID != id {
		return
	}
	now := time.Now()
	a.callStatus = callRuntimeStatus{State: state, Number: a.activeCall.Number, Detail: detail, Answered: answered, UpdatedAt: now}
	a.updateCallHistoryLocked(id, func(item *callHistoryItem) {
		item.Number = a.activeCall.Number
		item.State = state
		item.Detail = detail
		item.Answered = answered
	})
}

func (a *app) clearActiveCall(reason string) {
	a.callMu.Lock()
	if a.activeCall == nil {
		a.callMu.Unlock()
		return
	}
	call := a.activeCall
	number := call.Number
	call.cancel()
	a.activeCall = nil
	now := time.Now()
	a.callStatus = callRuntimeStatus{State: "通话结束", Number: number, Detail: reason, UpdatedAt: now}
	a.updateCallHistoryLocked(call.ID, func(item *callHistoryItem) {
		item.Number = number
		item.State = "通话结束"
		item.Detail = reason
		item.EndedAt = now
	})
	a.callMu.Unlock()
	if call.recording != nil {
		go a.finishCallRecording(*call.recording, number)
	}
}

func (a *app) updateCallHistoryLocked(id uint64, update func(*callHistoryItem)) {
	for index := len(a.callHistory) - 1; index >= 0; index-- {
		if a.callHistory[index].ID == id {
			update(&a.callHistory[index])
			return
		}
	}
}

func (a *app) markCallRecording(id uint64, name string) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.updateCallHistoryLocked(id, func(item *callHistoryItem) { item.RecordingName = name })
}

func (a *app) markCallRecordingForwarded(id uint64) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.updateCallHistoryLocked(id, func(item *callHistoryItem) { item.ForwardedToTelegram = true })
}

func (a *app) startCallRecording(id uint64, config callAutomationConfig) error {
	if a.modem != nil || a.demo {
		return errors.New("当前连接不支持模块原生录音")
	}
	if err := a.ensureUSBAT(); err != nil || a.usbAT == nil {
		if err != nil {
			return err
		}
		return errors.New("USB AT 设备不可用")
	}
	filename := callRecordingModuleFilename(id, time.Now())
	deleteResponse, deleteErr := a.runATCommand(fmt.Sprintf("AT+QFDEL=\"%s\"", filename), 3*time.Second)
	if deleteErr != nil {
		return fmt.Errorf("prepare recorder: %w", deleteErr)
	}
	if !atCommandSucceeded(deleteResponse) && !strings.Contains(strings.ToLower(deleteResponse), "not found") {
		log.Printf("previous recording delete returned %q; continuing", deleteResponse)
	}
	response, err := a.runATCommand(fmt.Sprintf("AT+QAUDRD=1,\"%s\",13,1", filename), 3*time.Second)
	if err != nil {
		return err
	}
	if !atCommandSucceeded(response) {
		return fmt.Errorf("QAUDRD: %s", strings.TrimSpace(response))
	}
	recording := &callRecording{
		callID:         id,
		moduleFilename: filename,
		directory:      a.callRecordingDirectory(config),
		startedAt:      time.Now(),
	}
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall == nil || a.activeCall.ID != id {
		return errors.New("通话已结束，未保存录音")
	}
	a.activeCall.recording = recording
	log.Printf("caller-side recording started as %s", filename)
	return nil
}

func (a *app) callRecordingDirectory(config callAutomationConfig) string {
	if config.RecordingDirectory != "" {
		return config.RecordingDirectory
	}
	a.automationMu.RLock()
	configPath := a.automationPath
	a.automationMu.RUnlock()
	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "recordings")
	}
	configDir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(configDir, "DJ 4G Hub", "recordings")
	}
	return "recordings"
}

func callRecordingModuleFilename(id uint64, timestamp time.Time) string {
	return fmt.Sprintf("dj4ghub_call_%s_%d.wav", timestamp.Format("20060102_150405"), id)
}

func (a *app) finishCallRecording(recording callRecording, number string) {
	if recording.command != nil {
		a.finishHostCallRecording(recording, number)
		return
	}
	response, err := a.runATCommand("AT+QAUDRD=0", 5*time.Second)
	if err != nil || !atCommandSucceeded(response) {
		log.Printf("call recording stop failed: response=%q err=%v", response, err)
		return
	}
	// Give the module a moment to finalize the WAV header and file length.
	time.Sleep(350 * time.Millisecond)
	listResponse, err := a.runATCommand(fmt.Sprintf("AT+QFLST=\"%s\"", recording.moduleFilename), 5*time.Second)
	if err != nil {
		log.Printf("call recording size lookup failed: %v", err)
		return
	}
	size, err := parseModuleFileSize(listResponse)
	if err != nil {
		log.Printf("call recording size lookup response invalid: %v", err)
		return
	}
	if err := a.ensureUSBAT(); err != nil || a.usbAT == nil {
		log.Printf("call recording download unavailable: %v", err)
		return
	}
	data, err := a.usbAT.DownloadFile(recording.moduleFilename, size, 45*time.Second)
	if err != nil {
		log.Printf("call recording download failed: %v", err)
		return
	}
	if err := os.MkdirAll(recording.directory, 0o700); err != nil {
		log.Printf("call recording create directory failed: %v", err)
		return
	}
	path := filepath.Join(recording.directory, recording.moduleFilename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("call recording save failed: %v", err)
		return
	}
	if deleteResponse, deleteErr := a.runATCommand(fmt.Sprintf("AT+QFDEL=\"%s\"", recording.moduleFilename), 3*time.Second); deleteErr != nil || !atCommandSucceeded(deleteResponse) {
		log.Printf("call recording saved but module cleanup failed: response=%q err=%v", deleteResponse, deleteErr)
	}
	log.Printf("caller-side recording saved to %s (%d bytes; caller %s)", path, len(data), displayCallNumber(number))
	a.markCallRecording(recording.callID, filepath.Base(path))
	go a.forwardCallRecording(path, number, recording.startedAt, recording.callID)
}

func (a *app) scheduleHostCallRecording(id uint64, ctx context.Context, config callAutomationConfig) {
	// Rebuilding the module UAC route plus the generated prompt takes about
	// twelve seconds. Start after that so recording does not contend with the
	// caller-facing prompt stream on the same device.
	if !waitForCall(ctx, 13*time.Second) {
		return
	}
	if err := a.startHostCallRecording(id, ctx, config); err != nil {
		log.Printf("host call recording did not start: %v", err)
		a.setCallStatus(id, "已接听", "录音未启动："+err.Error(), true)
	}
}

func (a *app) startHostCallRecording(id uint64, parent context.Context, config callAutomationConfig) error {
	directory := a.callRecordingDirectory(config)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create recording directory: %w", err)
	}
	filename := callRecordingModuleFilename(id, time.Now())
	outputPath := filepath.Join(directory, filename)
	rawPath := outputPath + ".raw"
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate recorder: %w", err)
	}
	recorder := filepath.Join(filepath.Dir(executable), "dj4ghub-uac-call-recorder")
	if _, err := os.Stat(recorder); err != nil {
		return fmt.Errorf("host recorder unavailable: %w", err)
	}
	recordContext, cancel := context.WithCancel(parent)
	command := exec.CommandContext(recordContext, recorder, rawPath, "120")
	if err := command.Start(); err != nil {
		cancel()
		return fmt.Errorf("start host recorder: %w", err)
	}
	recording := &callRecording{
		callID:     id,
		directory:  directory,
		startedAt:  time.Now(),
		rawPath:    rawPath,
		outputPath: outputPath,
		cancel:     cancel,
		command:    command,
	}
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.activeCall == nil || a.activeCall.ID != id {
		cancel()
		_ = command.Wait()
		return errors.New("通话已结束，未保存录音")
	}
	a.activeCall.recording = recording
	a.callStatus = callRuntimeStatus{State: "已接听", Number: a.activeCall.Number, Detail: "正在录制来电方语音", Answered: true, UpdatedAt: time.Now()}
	log.Printf("host caller-side recording started: %s", rawPath)
	return nil
}

func (a *app) finishHostCallRecording(recording callRecording, number string) {
	if recording.cancel != nil {
		recording.cancel()
	}
	if recording.command != nil {
		if err := recording.command.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("host recorder stopped: %v", err)
		}
	}
	stat, err := os.Stat(recording.rawPath)
	if err != nil {
		log.Printf("host recording output missing: %v", err)
		return
	}
	if stat.Size() == 0 {
		log.Printf("host recording contained no downlink audio")
		return
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil && runtime.GOOS == "darwin" {
		if _, statErr := os.Stat("/opt/homebrew/bin/ffmpeg"); statErr == nil {
			ffmpegPath = "/opt/homebrew/bin/ffmpeg"
			err = nil
		}
	}
	if err != nil {
		log.Printf("host recording cannot be finalized without ffmpeg: %v", err)
		return
	}
	command := exec.Command(
		ffmpegPath, "-nostdin", "-v", "error", "-y",
		"-f", "s16le", "-ar", "8000", "-ac", "1", "-i", recording.rawPath,
		"-c:a", "pcm_s16le", recording.outputPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		log.Printf("host recording WAV conversion failed: %v (%s)", err, strings.TrimSpace(string(output)))
		return
	}
	if err := os.Chmod(recording.outputPath, 0o600); err != nil {
		log.Printf("host recording permissions update failed: %v", err)
	}
	if err := os.Remove(recording.rawPath); err != nil {
		log.Printf("host recording raw cleanup failed: %v", err)
	}
	log.Printf("caller-side recording saved to %s (%d raw bytes; caller %s)", recording.outputPath, stat.Size(), displayCallNumber(number))
	a.markCallRecording(recording.callID, filepath.Base(recording.outputPath))
	go a.forwardCallRecording(recording.outputPath, number, recording.startedAt, recording.callID)
}

func (a *app) forwardCallRecording(path, number string, recordedAt time.Time, callID uint64) {
	config, err := a.automationSnapshot()
	if err != nil {
		log.Printf("call recording forwarding configuration unavailable: %v", err)
		return
	}
	if !config.Calls.ForwardRecordingsToTelegram {
		return
	}
	if recordedAt.IsZero() {
		recordedAt = time.Now()
	}
	caller := strings.TrimSpace(number)
	if caller == "" {
		caller = "未知号码"
	}
	caption := fmt.Sprintf("【DJ 4G Hub】来电录音\n号码：%s\n录制时间：%s", caller, recordedAt.Local().Format("2006-01-02 15:04:05"))
	forwarded := false
	for _, chatID := range config.SMS.Telegram.ChatIDs {
		if err := sendTelegramDocument(context.Background(), config.SMS.Telegram.BotToken, chatID, path, caption); err != nil {
			log.Printf("Telegram call recording forwarding to %s failed: %v", chatID, err)
			continue
		}
		forwarded = true
		log.Printf("call recording forwarded to Telegram chat %s", chatID)
	}
	if forwarded {
		a.markCallRecordingForwarded(callID)
	}
}

var moduleFileSizePattern = regexp.MustCompile(`(?m)^\+QFLST:\s*"[^"]+"\s*,\s*(\d+)`)

func parseModuleFileSize(response string) (int, error) {
	match := moduleFileSizePattern.FindStringSubmatch(response)
	if len(match) != 2 {
		return 0, fmt.Errorf("QFLST: %s", strings.TrimSpace(response))
	}
	size, err := strconv.Atoi(match[1])
	if err != nil || size < 44 {
		return 0, errors.New("录音文件大小无效")
	}
	return size, nil
}

func (a *app) answerCall() error {
	if a.modem != nil {
		return a.modem.AnswerCall()
	}
	var lastResponse string
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		response, err := a.runATCommand("ATA", 1500*time.Millisecond)
		lastResponse, lastErr = response, err
		if err == nil && callAnswerResponseSucceeded(response) {
			return nil
		}
		if callAnswerResponseRejected(response) {
			return fmt.Errorf("ATA: %s", strings.TrimSpace(response))
		}

		// Some QDC507 firmware echoes ATA but omits the final OK. Confirm the
		// state with CLCC instead of holding the USB AT channel for the whole call.
		stateResponse, stateErr := a.runATCommand("AT+CLCC", 2*time.Second)
		if stateErr == nil {
			calls := parseCLCCCalls(stateResponse)
			for _, call := range calls {
				if !call.Incoming {
					return nil
				}
			}
			if len(calls) == 0 {
				return fmt.Errorf("ATA 后没有活动通话: %s", strings.TrimSpace(stateResponse))
			}
		}
		if attempt == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("ATA 未确认接通: %s", strings.TrimSpace(lastResponse))
}

func callAnswerResponseSucceeded(response string) bool {
	if atCommandSucceeded(response) {
		return true
	}
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(response), "\r\n", "\n"))
	return normalized == "CONNECT" || strings.HasSuffix(normalized, "\nCONNECT") ||
		strings.Contains(normalized, "MO CONNECTED")
}

func callAnswerResponseRejected(response string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(response), "\r\n", "\n"))
	return atResponseIsError(normalized) || strings.Contains(normalized, "NO CARRIER") ||
		strings.Contains(normalized, "NO ANSWER") || strings.Contains(normalized, "BUSY")
}

func (a *app) hangupCall() error {
	if a.modem != nil {
		return a.modem.HangupCall()
	}
	response, err := a.runATCommand("ATH", 5*time.Second)
	if err != nil {
		return err
	}
	if !atCommandSucceeded(response) {
		return fmt.Errorf("ATH: %s", strings.TrimSpace(response))
	}
	return nil
}

func playPrompt(ctx context.Context, config callAutomationConfig, number string) error {
	if config.PromptFile == "" {
		return nil
	}
	if _, err := os.Stat(config.PromptFile); err != nil {
		return fmt.Errorf("提示音文件不可用: %w", err)
	}
	if config.PlaybackCommand != "" {
		command := strings.ReplaceAll(config.PlaybackCommand, "{{file}}", shellQuote(config.PromptFile))
		command = strings.ReplaceAll(command, "{{number}}", shellQuote(number))
		return exec.CommandContext(ctx, "sh", "-c", command).Run()
	}
	if runtime.GOOS == "darwin" {
		return exec.CommandContext(ctx, "afplay", config.PromptFile).Run()
	}
	return exec.CommandContext(ctx, "aplay", config.PromptFile).Run()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}

type clccCall struct {
	Number   string
	Incoming bool
}

var clccPattern = regexp.MustCompile(`\+CLCC:\s*\d+\s*,\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*[^,]*){2}(?:\s*,\s*"([^"]*)")?`)

func parseCLCCCalls(response string) []clccCall {
	matches := clccPattern.FindAllStringSubmatch(response, -1)
	result := make([]clccCall, 0, len(matches))
	for _, match := range matches {
		direction, _ := strconv.Atoi(match[1])
		state, _ := strconv.Atoi(match[2])
		result = append(result, clccCall{Number: normalizePhone(match[3]), Incoming: direction == 1 && (state == 4 || state == 5)})
	}
	return result
}

func (a *app) startDirectCallPoller(ctx context.Context) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.pollDirectCallOnce()
			timer.Reset(time.Second)
		}
	}
}

func (a *app) pollDirectCallOnce() {
	config, err := a.automationSnapshot()
	if err != nil || !config.Calls.Enabled || a.modem != nil || a.demo {
		return
	}
	response, err := a.runATCommand("AT+CLCC", 3*time.Second)
	if err != nil {
		return
	}
	calls := parseCLCCCalls(response)
	if len(calls) == 0 {
		a.clearActiveCall("通话已结束")
		return
	}
	for _, call := range calls {
		if call.Incoming {
			a.observeIncomingCall(call.Number)
		}
	}
}

func displayCallNumber(number string) string {
	if number == "" {
		return "未知号码"
	}
	return number
}
