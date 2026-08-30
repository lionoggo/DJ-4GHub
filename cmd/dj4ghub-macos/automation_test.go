package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCLCCCalls(t *testing.T) {
	response := "+CLCC: 1,1,4,0,0,\"+8613812345678\",145\r\nOK"
	calls := parseCLCCCalls(response)
	if len(calls) != 1 {
		t.Fatalf("parseCLCCCalls() count = %d, want 1", len(calls))
	}
	if !calls[0].Incoming {
		t.Fatal("parseCLCCCalls() incoming = false, want true")
	}
	if calls[0].Number != "+8613812345678" {
		t.Fatalf("parseCLCCCalls() number = %q", calls[0].Number)
	}
}

func TestWritePCM16MonoWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.wav")
	raw := []byte{0x00, 0x80, 0xff, 0x7f}
	if err := writePCM16MonoWAV(path, raw, 8000); err != nil {
		t.Fatalf("writePCM16MonoWAV() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 44+len(raw) || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("unexpected WAV header: %x", data[:44])
	}
	if got := binary.LittleEndian.Uint32(data[24:28]); got != 8000 {
		t.Fatalf("sample rate = %d, want 8000", got)
	}
	if got := binary.LittleEndian.Uint32(data[40:44]); got != uint32(len(raw)) {
		t.Fatalf("data size = %d, want %d", got, len(raw))
	}
}

func TestNormalizeLinuxPromptWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.wav")
	raw := make([]byte, 8)
	for index, sample := range []int16{0, 1000, 2000, 3000} {
		binary.LittleEndian.PutUint16(raw[index*2:index*2+2], uint16(sample))
	}
	if err := writePCM16MonoWAV(path, raw, 16000); err != nil {
		t.Fatal(err)
	}
	if err := normalizeLinuxPromptWAV(path); err != nil {
		t.Fatalf("normalizeLinuxPromptWAV() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(data[24:28]); got != 8000 {
		t.Fatalf("sample rate = %d, want 8000", got)
	}
	if got := binary.LittleEndian.Uint32(data[40:44]); got != 8004 {
		t.Fatalf("data size = %d, want 8004", got)
	}
	if first := int16(binary.LittleEndian.Uint16(data[44:46])); first != 0 {
		t.Fatalf("first output sample = %d, want 0", first)
	}
	if firstSpeech := int16(binary.LittleEndian.Uint16(data[44+8000 : 46+8000])); firstSpeech != 0 {
		t.Fatalf("first speech sample = %d, want 0", firstSpeech)
	}
	if secondSpeech := int16(binary.LittleEndian.Uint16(data[46+8000 : 48+8000])); secondSpeech != 2000 {
		t.Fatalf("second speech sample = %d, want 2000", secondSpeech)
	}
}

func TestParseCLCCCallsActiveCallIsNotIncoming(t *testing.T) {
	response := "+CLCC: 1,1,0,0,0,\"+8613812345678\",145\r\nOK"
	calls := parseCLCCCalls(response)
	if len(calls) != 1 || calls[0].Incoming {
		t.Fatalf("parseCLCCCalls() = %#v, want active non-incoming call", calls)
	}
}

func TestParseCLCCCallsIgnoresLTEDataSession(t *testing.T) {
	response := "+CLCC: 1,0,0,1,0,\"\",128\r\nOK"
	if calls := parseCLCCCalls(response); len(calls) != 0 {
		t.Fatalf("parseCLCCCalls() = %#v, want no voice calls", calls)
	}
}

func TestParseCLCCCallsKeepsVoiceAlongsideLTEDataSession(t *testing.T) {
	response := "+CLCC: 1,0,0,1,0,\"\",128\r\n+CLCC: 2,1,4,0,0,\"+8613812345678\",145\r\nOK"
	calls := parseCLCCCalls(response)
	if len(calls) != 1 || !calls[0].Incoming || calls[0].Number != "+8613812345678" {
		t.Fatalf("parseCLCCCalls() = %#v, want one incoming voice call", calls)
	}
}

func TestNormalizeAutomationConfig(t *testing.T) {
	config := defaultAutomationConfig()
	config.SMS.RecipientNumbers = []string{" +86 138-1234-5678 ", "+8613812345678"}
	config.SMS.SenderAllowlist = []string{"10086", " 10010 "}
	config.Calls.AllowedNumbers = []string{" +86 138-1234-5678 "}
	config.Calls.PromptFile = "/tmp/answer.wav"
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() error = %v", err)
	}
	if got := strings.Join(config.SMS.RecipientNumbers, ","); got != "+8613812345678" {
		t.Fatalf("recipient numbers = %q", got)
	}
	if got := strings.Join(config.SMS.SenderAllowlist, ","); got != "10010,10086" {
		t.Fatalf("sender allowlist = %q", got)
	}
	if !numberAllowed("+86 138 1234 5678", config.Calls.AllowedNumbers) {
		t.Fatal("numberAllowed() did not normalize a formatted incoming number")
	}
}

func TestNormalizeAutomationConfigRejectsIncompleteTelegram(t *testing.T) {
	config := defaultAutomationConfig()
	config.SMS.Telegram.Enabled = true
	config.SMS.Telegram.ChatIDs = []string{"12345"}
	if err := normalizeAutomationConfig(&config); err == nil {
		t.Fatal("normalizeAutomationConfig() accepted Telegram without Bot Token")
	}
}

func TestNormalizeAutomationConfigAllowsRecordingForwardWithoutSMSForwarding(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.RecordCalls = true
	config.Calls.RecordingNoticeOK = true
	config.Calls.ForwardRecordingsToTelegram = true
	config.SMS.Telegram.BotToken = "123:recording-token"
	config.SMS.Telegram.ChatIDs = []string{"12345"}
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() rejected recording-only Telegram forwarding: %v", err)
	}
	if config.SMS.Telegram.Enabled {
		t.Fatal("recording-only forwarding unexpectedly enabled SMS forwarding")
	}
}

func TestNormalizeAutomationConfigAllowsFeishuAppBot(t *testing.T) {
	config := defaultAutomationConfig()
	config.SMS.Feishu = feishuForwardConfig{
		Enabled:         true,
		Mode:            feishuModeAppBot,
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		RecipientIDType: "email",
		RecipientID:     "receiver@example.com",
		APIBaseURL:      "https://fsopen.bytedance.net/",
	}
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() rejected Feishu app bot: %v", err)
	}
	if config.SMS.Feishu.APIBaseURL != "https://fsopen.bytedance.net" {
		t.Fatalf("API base URL = %q, want trimmed URL", config.SMS.Feishu.APIBaseURL)
	}
}

func TestSendFeishuAppBotMessage(t *testing.T) {
	var tokenRequest map[string]string
	var messageRequest map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			if err := json.NewDecoder(r.Body).Decode(&tokenRequest); err != nil {
				t.Fatalf("decode token request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tenant-token"})
		case "/open-apis/im/v1/messages":
			if r.URL.Query().Get("receive_id_type") != "email" {
				t.Fatalf("receive_id_type = %q, want email", r.URL.Query().Get("receive_id_type"))
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&messageRequest); err != nil {
				t.Fatalf("decode message request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	config := feishuForwardConfig{
		Mode:            feishuModeAppBot,
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		RecipientIDType: "email",
		RecipientID:     "receiver@example.com",
		APIBaseURL:      server.URL,
	}
	if err := sendFeishuAppBotMessage(context.Background(), config, "短信测试"); err != nil {
		t.Fatalf("sendFeishuAppBotMessage() error = %v", err)
	}
	if tokenRequest["app_id"] != "cli_test" || tokenRequest["app_secret"] != "app-secret" {
		t.Fatalf("unexpected token request: %#v", tokenRequest)
	}
	if messageRequest["receive_id"] != "receiver@example.com" || messageRequest["msg_type"] != "text" {
		t.Fatalf("unexpected message request: %#v", messageRequest)
	}
	if messageRequest["content"] != `{"text":"短信测试"}` {
		t.Fatalf("message content = %q", messageRequest["content"])
	}
}

func TestSendFeishuAudioMessage(t *testing.T) {
	opusPath := filepath.Join(t.TempDir(), "call.opus")
	if err := os.WriteFile(opusPath, []byte("opus-recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	var messageRequest map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tenant-token"})
		case "/open-apis/im/v1/files":
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("upload Authorization = %q", got)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			if got := r.FormValue("file_type"); got != "opus" {
				t.Fatalf("file_type = %q, want opus", got)
			}
			if got := r.FormValue("duration"); got != "1250" {
				t.Fatalf("duration = %q, want 1250", got)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("FormFile(file) error = %v", err)
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil {
				t.Fatal(err)
			}
			if header.Filename != "call.opus" || string(data) != "opus-recording" {
				t.Fatalf("uploaded audio = %q / %q", header.Filename, data)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"file_key": "file-test"}})
		case "/open-apis/im/v1/messages":
			if r.URL.Query().Get("receive_id_type") != "email" {
				t.Fatalf("receive_id_type = %q", r.URL.Query().Get("receive_id_type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&messageRequest); err != nil {
				t.Fatalf("decode audio message: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	config := feishuForwardConfig{
		Mode:            feishuModeAppBot,
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		RecipientIDType: "email",
		RecipientID:     "receiver@example.com",
		APIBaseURL:      server.URL,
	}
	if err := sendFeishuAudioMessage(context.Background(), config, opusPath, 1250); err != nil {
		t.Fatalf("sendFeishuAudioMessage() error = %v", err)
	}
	if messageRequest["msg_type"] != "audio" || messageRequest["receive_id"] != "receiver@example.com" {
		t.Fatalf("audio message = %#v", messageRequest)
	}
	if messageRequest["content"] != `{"duration":1250,"file_key":"file-test"}` {
		t.Fatalf("audio content = %q", messageRequest["content"])
	}
}

func TestWAVDurationMS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one-second.wav")
	if err := writePCM16MonoWAV(path, make([]byte, 16000), 8000); err != nil {
		t.Fatal(err)
	}
	duration, err := wavDurationMS(path)
	if err != nil {
		t.Fatalf("wavDurationMS() error = %v", err)
	}
	if duration != 1000 {
		t.Fatalf("wavDurationMS() = %d, want 1000", duration)
	}
}

func TestNormalizeAutomationConfigRejectsRecordingForwardWithoutRecording(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.ForwardRecordingsToTelegram = true
	config.SMS.Telegram.BotToken = "123:recording-token"
	config.SMS.Telegram.ChatIDs = []string{"12345"}
	if err := normalizeAutomationConfig(&config); err == nil {
		t.Fatal("normalizeAutomationConfig() allowed forwarding with recording disabled")
	}
}

func TestNormalizeAutomationConfigAllowsFeishuRecordingForwardWithoutSMSForwarding(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.RecordCalls = true
	config.Calls.RecordingNoticeOK = true
	config.Calls.ForwardRecordingsToFeishu = true
	config.SMS.Feishu = feishuForwardConfig{
		Mode:            feishuModeAppBot,
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		RecipientIDType: "email",
		RecipientID:     "receiver@example.com",
		APIBaseURL:      "https://open.feishu.cn",
	}
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() rejected Feishu recording-only forwarding: %v", err)
	}
	if config.SMS.Feishu.Enabled {
		t.Fatal("recording-only forwarding unexpectedly enabled SMS forwarding")
	}
}

func TestRenderSMSForwardText(t *testing.T) {
	item := receivedSMS{
		Sender:    "10086",
		Content:   "验证码是 482913",
		Code:      "482913",
		Timestamp: time.Date(2026, 8, 30, 12, 34, 56, 0, time.Local),
	}
	got := renderSMSForwardText("{{sender}}|{{content}}|{{code}}", item)
	if got != "10086|验证码是 482913|482913" {
		t.Fatalf("renderSMSForwardText() = %q", got)
	}
}

func TestModulePromptPlaybackUsesFarEndAudioRoute(t *testing.T) {
	commands := modulePromptPlaybackCommands()
	if len(commands) == 0 {
		t.Fatal("modulePromptPlaybackCommands() returned no commands")
	}
	for _, command := range commands {
		if !strings.HasSuffix(command, ",0,1,1") {
			t.Fatalf("module prompt command = %q, want far-end QPSND route", command)
		}
	}
}

func TestNormalizeAutomationConfigRequiresRecordingNotice(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.RecordCalls = true
	if err := normalizeAutomationConfig(&config); err == nil {
		t.Fatal("normalizeAutomationConfig() allowed recording without a notice confirmation")
	}
	config.Calls.RecordingNoticeOK = true
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() rejected acknowledged recording: %v", err)
	}
}

func TestNormalizeAutomationConfigAcceptsTypedPrompt(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.PromptText = "  请留言。  "
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() typed prompt error = %v", err)
	}
	if config.Calls.PromptText != "请留言。" {
		t.Fatalf("prompt text = %q", config.Calls.PromptText)
	}
}

func TestNormalizeAutomationConfigRequiresFileForFilePromptSource(t *testing.T) {
	config := defaultAutomationConfig()
	config.Calls.PromptSource = promptSourceFile
	if err := normalizeAutomationConfig(&config); err == nil {
		t.Fatal("normalizeAutomationConfig() allowed a file prompt source without a file")
	}
	config.Calls.PromptFile = "/tmp/answer.wav"
	if err := normalizeAutomationConfig(&config); err != nil {
		t.Fatalf("normalizeAutomationConfig() rejected a file prompt source: %v", err)
	}
}

func TestCallPromptWarmDelay(t *testing.T) {
	warmDelay, ok := callPromptWarmDelay(10 * time.Second)
	if !ok || warmDelay != 8*time.Second {
		t.Fatalf("callPromptWarmDelay(10s) = %v, %v; want 8s, true", warmDelay, ok)
	}
	if warmDelay, ok := callPromptWarmDelay(2 * time.Second); ok || warmDelay != 0 {
		t.Fatalf("callPromptWarmDelay(2s) = %v, %v; want 0, false", warmDelay, ok)
	}
}

func TestCallPromptWarmDelayUsesConfiguredLead(t *testing.T) {
	warmDelay, ok := callPromptWarmDelay(5 * time.Second)
	if !ok || warmDelay != 3*time.Second {
		t.Fatalf("callPromptWarmDelay(5s) = %v, %v; want 3s, true", warmDelay, ok)
	}
}

func TestAutomationAPIFilePromptSourcePreservesPreparedAudio(t *testing.T) {
	instance := &app{automationPath: filepath.Join(t.TempDir(), "automation.json")}
	incoming := automationConfig{
		SMS: smsForwardConfig{TextTemplate: defaultSMSForwardTemplate},
		Calls: callAutomationConfig{
			AnswerAfterSeconds: 2,
			HangupAfterSeconds: 12,
			PromptSource:       promptSourceFile,
			PromptText:         "这是用于说明当前提示语的文本。",
			PromptFile:         "/tmp/prepared-answer.wav",
		},
	}
	body, err := json.Marshal(incoming)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	instance.updateAutomation(response, httptest.NewRequest(http.MethodPut, "/api/automation", strings.NewReader(string(body))))
	if response.Code != http.StatusOK {
		t.Fatalf("updateAutomation() status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := instance.automationSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Calls.PromptSource != promptSourceFile || stored.Calls.PromptFile != "/tmp/prepared-answer.wav" {
		t.Fatalf("prepared prompt was changed: source=%q file=%q", stored.Calls.PromptSource, stored.Calls.PromptFile)
	}
}

func TestParseModuleFileSize(t *testing.T) {
	size, err := parseModuleFileSize("+QFLST: \"dj4ghub_call.wav\",32768\r\nOK")
	if err != nil || size != 32768 {
		t.Fatalf("parseModuleFileSize() = %d, %v", size, err)
	}
}

func TestCallRecordingModuleFilename(t *testing.T) {
	got := callRecordingModuleFilename(7, time.Date(2026, 8, 30, 12, 34, 56, 0, time.Local))
	if got != "dj4ghub_call_20260830_123456_7.wav" {
		t.Fatalf("callRecordingModuleFilename() = %q", got)
	}
}

func TestCallsAPIListsCurrentHistoryAndRecordings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "automation.json")
	recordingDirectory := filepath.Join(filepath.Dir(configPath), "recordings")
	if err := os.MkdirAll(recordingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	filename := "dj4ghub_call_20260830_143000_1.wav"
	if err := os.WriteFile(filepath.Join(recordingDirectory, filename), []byte("RIFF"+strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 14, 30, 0, 0, time.Local)
	instance := &app{
		automationPath: configPath,
		callStatus:     callRuntimeStatus{State: "通话结束", Number: "10086", Detail: "已自动挂断", UpdatedAt: now},
		callHistory: []callHistoryItem{{
			ID:                  1,
			Number:              "10086",
			State:               "通话结束",
			StartedAt:           now,
			RecordingName:       filename,
			ForwardedToTelegram: true,
		}},
	}
	response := httptest.NewRecorder()
	instance.getCalls(response, httptest.NewRequest(http.MethodGet, "/api/calls", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("getCalls() status = %d, body=%s", response.Code, response.Body.String())
	}
	var result callsView
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Active || len(result.History) != 1 || len(result.Recordings) != 1 {
		t.Fatalf("getCalls() = %#v", result)
	}
	if result.Recordings[0].Name != filename || !result.Recordings[0].ForwardedToTelegram {
		t.Fatalf("recording view = %#v", result.Recordings[0])
	}

	download := httptest.NewRequest(http.MethodGet, "/api/calls/recordings/"+filename, nil)
	download.SetPathValue("name", filename)
	downloadResponse := httptest.NewRecorder()
	instance.downloadCallRecording(downloadResponse, download)
	if downloadResponse.Code != http.StatusOK {
		t.Fatalf("downloadCallRecording() status = %d, body=%s", downloadResponse.Code, downloadResponse.Body.String())
	}
	if got := downloadResponse.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("recording Content-Type = %q", got)
	}
	if !strings.Contains(downloadResponse.Body.String(), "RIFF") {
		t.Fatal("downloadCallRecording() did not serve the WAV data")
	}
}

func TestCallRecordingNameRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../secret.wav", "other.wav", "dj4ghub_call_2026.wav.raw"} {
		if isCallRecordingName(name) {
			t.Fatalf("isCallRecordingName(%q) = true", name)
		}
	}
}

func TestCallControlRejectsActionsWithoutAnActiveCall(t *testing.T) {
	instance := &app{automationPath: filepath.Join(t.TempDir(), "automation.json")}
	answer := httptest.NewRecorder()
	instance.answerActiveCall(answer, httptest.NewRequest(http.MethodPost, "/api/calls/answer", nil))
	if answer.Code != http.StatusConflict {
		t.Fatalf("answerActiveCall() status = %d, body=%s", answer.Code, answer.Body.String())
	}
	hangup := httptest.NewRecorder()
	instance.hangupActiveCall(hangup, httptest.NewRequest(http.MethodPost, "/api/calls/hangup", nil))
	if hangup.Code != http.StatusConflict {
		t.Fatalf("hangupActiveCall() status = %d, body=%s", hangup.Code, hangup.Body.String())
	}
}

func TestSendTelegramDocumentUsesMultipartAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.wav")
	if err := os.WriteFile(path, []byte("RIFFtest-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		if got := r.FormValue("chat_id"); got != "12345" {
			t.Fatalf("chat_id = %q", got)
		}
		if got := r.FormValue("caption"); got != "来电录音" {
			t.Fatalf("caption = %q", got)
		}
		file, header, err := r.FormFile("document")
		if err != nil {
			t.Fatalf("FormFile(document) error = %v", err)
		}
		defer file.Close()
		if header.Filename != "call.wav" {
			t.Fatalf("filename = %q", header.Filename)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]byte, len(data))
		if _, err := file.Read(actual); err != nil {
			t.Fatalf("read uploaded document: %v", err)
		}
		if string(actual) != string(data) {
			t.Fatalf("uploaded document = %q, want %q", actual, data)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := sendTelegramDocumentToEndpoint(context.Background(), server.URL, "12345", path, "来电录音"); err != nil {
		t.Fatalf("sendTelegramDocumentToEndpoint() error = %v", err)
	}
}

func TestAutomationAPIHidesAndPreservesSecrets(t *testing.T) {
	instance := &app{automationPath: filepath.Join(t.TempDir(), "automation.json")}
	initial := automationConfig{
		SMS: smsForwardConfig{
			Enabled: true,
			Telegram: telegramForwardConfig{
				Enabled:  true,
				BotToken: "123:secret-token",
				ChatIDs:  []string{"123456"},
			},
		},
		Calls: callAutomationConfig{AnswerAfterSeconds: 2, HangupAfterSeconds: 12},
	}
	body, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/automation", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	instance.updateAutomation(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("updateAutomation() status = %d, body=%s", response.Code, response.Body.String())
	}

	getResponse := httptest.NewRecorder()
	instance.getAutomation(getResponse, httptest.NewRequest(http.MethodGet, "/api/automation", nil))
	if strings.Contains(getResponse.Body.String(), "secret-token") {
		t.Fatalf("getAutomation() exposed a saved secret: %s", getResponse.Body.String())
	}
	if !strings.Contains(getResponse.Body.String(), `"bot_token_set":true`) {
		t.Fatalf("getAutomation() did not report that a token is present: %s", getResponse.Body.String())
	}

	initial.SMS.Telegram.BotToken = ""
	body, err = json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	instance.updateAutomation(response, httptest.NewRequest(http.MethodPut, "/api/automation", strings.NewReader(string(body))))
	if response.Code != http.StatusOK {
		t.Fatalf("blank-token update status = %d, body=%s", response.Code, response.Body.String())
	}
	config, err := instance.automationSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if config.SMS.Telegram.BotToken != "123:secret-token" {
		t.Fatalf("blank-token update erased existing token: %q", config.SMS.Telegram.BotToken)
	}
}
