package codexselection

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestManualCallbackKeepsKeyOutOfSerializableStatusAndConsumesSession(t *testing.T) {
	service := New()
	t.Cleanup(service.Close)
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := connectCallbackAndState(t, started.ConnectURL)
	if callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/callback" || callback.Port() == "" {
		t.Fatalf("callback URL = %s, want a random IPv4 loopback callback", callback)
	}
	if len(state) < 43 {
		t.Fatalf("state length = %d, want a 256-bit base64url state", len(state))
	}

	secret := "sk-selection-secret-never-in-status"
	manual := callbackWithPayload(callback, state, callbackPayload{BaseURL: "https://gateway.example.test/v1", APIKey: secret, KeyName: "Work key"})
	ready := service.CompleteManualCallback(started.State.SessionID, manual)
	if ready.Status != "ready" || ready.KeyName != "Work key" || ready.BaseURL != "https://gateway.example.test/v1" {
		t.Fatalf("completion state = %+v", ready)
	}
	encoded, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "payload") {
		t.Fatalf("serializable status leaked a credential: %s", encoded)
	}

	used := ""
	status, err := service.WithCredential(started.State.SessionID, true, func(credential Credential) error {
		used = string(credential.APIKey)
		if credential.BaseURL != "https://gateway.example.test/v1" || credential.KeyName != "Work key" {
			t.Fatalf("credential metadata = %+v", credential)
		}
		return nil
	})
	if err != nil || used != secret || status.Status != "applied" {
		t.Fatalf("WithCredential() = %+v, %v; key match=%t", status, err, used == secret)
	}
	if repeated := service.CompleteManualCallback(started.State.SessionID, manual); repeated.Status != "expired" {
		t.Fatalf("consumed callback status = %+v, want expired", repeated)
	}
}

func TestCallbackRejectsWrongStateAndForeignHostWithoutConsumingSession(t *testing.T) {
	service := New()
	t.Cleanup(service.Close)
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := connectCallbackAndState(t, started.ConnectURL)
	foreign := callbackWithPayload(callback, state, callbackPayload{BaseURL: "https://evil.example/v1", APIKey: "sk-foreign", KeyName: "Foreign"})
	if result := service.CompleteManualCallback(started.State.SessionID, foreign); result.Status != "pending" {
		t.Fatalf("foreign host callback = %+v, want pending rejection", result)
	}
	wrongState := callbackWithPayload(callback, "wrong-state", callbackPayload{BaseURL: "https://gateway.example.test/v1", APIKey: "sk-good", KeyName: "Good"})
	if result := service.CompleteManualCallback(started.State.SessionID, wrongState); result.Status != "pending" {
		t.Fatalf("wrong-state callback = %+v, want pending rejection", result)
	}
	valid := callbackWithPayload(callback, state, callbackPayload{BaseURL: "https://gateway.example.test/v1", APIKey: "sk-good", KeyName: "Good"})
	if result := service.CompleteManualCallback(started.State.SessionID, valid); result.Status != "ready" {
		t.Fatalf("valid callback after failures = %+v", result)
	}
}

func TestBrowserCallbackUsesLoopbackRemoteAddressAndClearsFragmentPage(t *testing.T) {
	service := New()
	t.Cleanup(service.Close)
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := connectCallbackAndState(t, started.ConnectURL)
	page, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(page.Body)
	page.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	pageSource := string(body)
	if page.StatusCode != http.StatusOK || page.Header.Get("Cache-Control") != "no-store" || page.Header.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(pageSource, "history.replaceState") {
		t.Fatalf("callback page response = %d %#v %s", page.StatusCode, page.Header, body)
	}
	for _, marker := range []string{"const rawCallbackURL=location.href", `id="retry-callback"`, `id="copy-callback"`, "navigator.clipboard.writeText(rawCallbackURL)", "document.execCommand(\"copy\")"} {
		if !strings.Contains(pageSource, marker) {
			t.Fatalf("callback page is missing fallback marker %q", marker)
		}
	}
	if strings.Index(pageSource, "history.replaceState") > strings.Index(pageSource, `fetch("/complete"`) {
		t.Fatal("callback page must clear the URL fragment before posting completion")
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "console.", "innerHTML", "document.write", "callback_url"} {
		if strings.Contains(pageSource, forbidden) {
			t.Fatalf("callback page must not expose callback material through %q", forbidden)
		}
	}

	payload := encodedPayload(t, callbackPayload{BaseURL: "https://gateway.example.test/v1", APIKey: "sk-browser", KeyName: "Browser"})
	request, err := http.NewRequest(http.MethodPost, callback.Scheme+"://"+callback.Host+"/complete", strings.NewReader(`{"payload":"`+payload+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-XIASS-Selection-State", state)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("browser completion HTTP status = %d", response.StatusCode)
	}
	if result := service.Status(started.State.SessionID); result.Status != "ready" || result.KeyName != "Browser" {
		t.Fatalf("browser completion status = %+v", result)
	}
}

func TestBrowserCompletionKeepsResponseAliveDuringLoopbackShutdown(t *testing.T) {
	for attempt := 0; attempt < 24; attempt++ {
		service := New()
		started, err := service.Begin("https://gateway.example.test")
		if err != nil {
			service.Close()
			t.Fatalf("attempt %d: Begin() error = %v", attempt, err)
		}
		callback, state := connectCallbackAndState(t, started.ConnectURL)
		payload := encodedPayload(t, callbackPayload{
			BaseURL: "https://gateway.example.test/v1",
			APIKey:  "sk-loopback-response",
			KeyName: "Loopback",
		})
		request, err := http.NewRequest(http.MethodPost, callback.Scheme+"://"+callback.Host+"/complete", strings.NewReader(`{"payload":"`+payload+`"}`))
		if err != nil {
			service.Close()
			t.Fatalf("attempt %d: NewRequest() error = %v", attempt, err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-XIASS-Selection-State", state)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			service.Close()
			t.Fatalf("attempt %d: browser completion error = %v", attempt, err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			service.Close()
			t.Fatalf("attempt %d: browser response body error = %v", attempt, readErr)
		}
		if response.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
			service.Close()
			t.Fatalf("attempt %d: browser completion response = %d %q", attempt, response.StatusCode, body)
		}
		waitForLoopbackListenerToClose(t, callback.Host)
		service.Close()
	}
}

func TestCallbackHandlerRejectsNonLoopbackRemoteAddress(t *testing.T) {
	service := New()
	t.Cleanup(service.Close)
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, _ := connectCallbackAndState(t, started.ConnectURL)
	request := httptest.NewRequest(http.MethodGet, callback.String(), nil)
	request.Host = callback.Host
	request.RemoteAddr = "203.0.113.7:4040"
	recorder := httptest.NewRecorder()
	service.handler(started.State.SessionID).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-loopback callback status = %d, want 403", recorder.Code)
	}
}

func TestCancelClosesReservedListener(t *testing.T) {
	service := New()
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, _ := connectCallbackAndState(t, started.ConnectURL)
	if result := service.Cancel(started.State.SessionID); result.Status != "cancelled" {
		t.Fatalf("Cancel() = %+v", result)
	}
	connection, err := net.DialTimeout("tcp", callback.Host, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatalf("callback listener %s still accepts connections after cancel", callback.Host)
	}
}

func waitForLoopbackListenerToClose(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			return
		}
		_ = connection.Close()
		if time.Now().After(deadline) {
			t.Fatalf("callback listener %s still accepts connections after browser completion", address)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func connectCallbackAndState(t *testing.T, connectURL string) (*url.URL, string) {
	t.Helper()
	parsed, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(parsed.Query().Get("callback"))
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("missing callback state")
	}
	return callback, state
}

func callbackWithPayload(callback *url.URL, state string, payload callbackPayload) string {
	result := *callback
	result.Fragment = url.Values{"state": []string{state}, "payload": []string{encodedPayload(nil, payload)}}.Encode()
	return result.String()
}

func encodedPayload(t *testing.T, payload callbackPayload) string {
	if t != nil {
		t.Helper()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSpace(data))
}
