// Package apiserver exposes the Sender over a small local HTTP API so scripts and other
// programs can send ilink messages without shelling out to the CLI.
package apiserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal"
	ilink "github.com/openilink/openilink-sdk-go"

	"github.com/phpgao/ilinkgo/internal/sender"
	"github.com/phpgao/ilinkgo/internal/store"
)

// Config configures the API server.
type Config struct {
	Listen string // e.g. "127.0.0.1:9100"
	Token  string // required bearer token; auth is disabled only if empty
	// OnLoggedIn is called once a QR login started through /api/login succeeds, so the
	// caller can (re)start whatever needs a valid session (e.g. the message monitor).
	OnLoggedIn func()
}

// Serve starts the HTTP API and blocks until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, cfg Config, s *sender.Sender, logger *log.Logger) error {
	if cfg.Token == "" {
		logger.Println("[api] WARN: no bearer token configured, /api endpoints are unauthenticated")
	}

	h := &handler{sender: s, token: cfg.Token, log: logger, srvCtx: ctx, onLoggedIn: cfg.OnLoggedIn}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.Handle("/api/login", h.auth(h.login))
	mux.Handle("/api/send/text", h.auth(h.sendText))
	mux.Handle("/api/send/image", h.auth(h.sendImage))
	mux.Handle("/api/send/video", h.auth(h.sendVideo))
	mux.Handle("/api/send/file", h.auth(h.sendFile))
	mux.Handle("/api/send/media", h.auth(h.sendMedia))
	mux.Handle("/api/send/typing", h.auth(h.sendTyping))

	// Listen up front so a bound-port error surfaces immediately, not on first request.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("apiserver: listen %s: %w", cfg.Listen, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	logger.Printf("[api] listening on %s", cfg.Listen)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type handler struct {
	sender     *sender.Sender
	token      string
	log        *log.Logger
	srvCtx     context.Context // server lifetime; login flows outlive the triggering request
	onLoggedIn func()

	loginMu     sync.Mutex
	loginStatus string // "", wait, scanned, confirmed, error
	loginURL    string
	loginQR     string
	loginMsg    string
	loginBusy   bool
}

func (h *handler) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.token != "" {
			const prefix = "Bearer "
			got := r.Header.Get("Authorization")
			if !strings.HasPrefix(got, prefix) ||
				subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(got, prefix)), []byte(h.token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "missing or invalid bearer token"})
				return
			}
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "use POST"})
			return
		}
		next(w, r)
	})
}

// checkSession makes sure a login session is available before sending. Before the first
// login (or after the session is gone) the send endpoints fail with a clear message
// instead of an opaque upstream error.
func (h *handler) checkSession(w http.ResponseWriter) bool {
	if h.sender.Client.Token() != "" {
		return true
	}
	if st := h.sender.Store; st != nil {
		cred, err := st.LoadCredential()
		if err == nil && cred != nil && cred.Token != "" {
			h.sender.Client.SetToken(cred.Token)
			if cred.BaseURL != "" {
				h.sender.Client.SetBaseURL(cred.BaseURL)
			}
			return true
		}
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"ok": false, "error": "not logged in: POST /api/login to get the bind URL, then scan it in WeChat",
	})
	return false
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ts": time.Now().Unix()})
}

// login starts (or reports on) a QR login. It returns the WeChat bind URL so a
// containerized deployment with no terminal can still complete the login by opening
// the URL in a browser, and repeats the URL on every call until the scan completes.
func (h *handler) login(w http.ResponseWriter, _ *http.Request) {
	status, bindURL, qr, msg := h.ensureLogin()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": status, "url": bindURL, "qr_ascii": qr, "message": msg,
	})
}

// ensureLogin kicks off the QR flow if none is running and returns its current state.
// It blocks briefly for the first QR URL so callers get a usable URL in the response.
func (h *handler) ensureLogin() (status, bindURL, qr, msg string) {
	h.loginMu.Lock()
	if h.loginBusy || h.loginStatus == "wait" || h.loginStatus == "scanned" {
		h.loginMu.Unlock()
		return h.loginSnapshot()
	}
	h.loginBusy = true
	h.loginStatus, h.loginURL, h.loginQR, h.loginMsg = "starting", "", "", ""
	h.loginMu.Unlock()

	urlCh := make(chan string, 1)
	go h.runLogin(urlCh)

	select {
	case <-urlCh:
		return h.loginSnapshot()
	case <-time.After(20 * time.Second):
		return h.loginSnapshot()
	}
}

func (h *handler) loginSnapshot() (status, bindURL, qr, msg string) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	return h.loginStatus, h.loginURL, h.loginQR, h.loginMsg
}

func (h *handler) setLogin(status, bindURL, qr, msg string) {
	h.loginMu.Lock()
	h.loginStatus, h.loginURL, h.loginQR, h.loginMsg = status, bindURL, qr, msg
	h.loginMu.Unlock()
}

func (h *handler) runLogin(urlCh chan<- string) {
	defer func() {
		h.loginMu.Lock()
		h.loginBusy = false
		h.loginMu.Unlock()
	}()

	result, err := h.sender.Client.LoginWithQR(h.srvCtx, &ilink.LoginCallbacks{
		OnQRCode: func(imgContent string) {
			// qrcode_img_content is the bind URL; also render it as a terminal QR.
			h.setLogin("wait", imgContent, renderQR(imgContent), "")
			select {
			case urlCh <- imgContent:
			default:
			}
		},
		OnScanned: func() {
			h.setLogin("scanned", h.loginURL, h.loginQR, "scanned, confirm on WeChat")
			select {
			case urlCh <- h.loginURL:
			default:
			}
		},
		OnExpired: func(attempt, max int) {
			h.setLogin("wait", "", "", fmt.Sprintf("QR expired, refreshing (%d/%d)", attempt, max))
		},
	})
	if err != nil {
		h.log.Printf("[api] login failed: %v", err)
		h.setLogin("error", "", "", err.Error())
		return
	}
	if result == nil || !result.Connected {
		msg := "login incomplete"
		if result != nil {
			msg = result.Message
		}
		h.setLogin("error", "", "", msg)
		return
	}

	st := h.sender.Store
	if st != nil {
		if err := st.SaveCredential(&store.Credential{
			Token:   h.sender.Client.Token(),
			BaseURL: h.sender.Client.BaseURL(),
			BotID:   result.BotID,
			UserID:  result.UserID,
		}); err != nil {
			h.log.Printf("[api] save credential: %v", err)
			h.setLogin("error", "", "", err.Error())
			return
		}
	}

	h.log.Printf("[api] login confirmed: BotID=%s UserID=%s", result.BotID, result.UserID)
	h.setLogin("confirmed", "", "", "logged in")
	if h.onLoggedIn != nil {
		h.onLoggedIn()
	}
}

// renderQR encodes the bind URL as a terminal QR, so `curl | jq -r .qr_ascii` is enough
// to scan from a shell.
func renderQR(text string) string {
	if text == "" {
		return ""
	}
	var buf strings.Builder
	qrterminal.GenerateHalfBlock(text, qrterminal.L, &buf)
	return buf.String()
}

type textReq struct {
	To           string `json:"to"` // optional: defaults to the captured user
	Text         string `json:"text"`
	ContextToken string `json:"context_token"`
}

func (h *handler) sendText(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req textReq
	if !decode(w, r, &req) {
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "text is required"})
		return
	}
	clientID, err := h.sender.SendText(r.Context(), req.To, req.Text, req.ContextToken)
	h.respond(w, clientID, err)
}

// mediaReq covers image/video/file/media: all take a local file_path readable by this
// process, an optional caption, and an optional explicit context token.
type mediaReq struct {
	To           string `json:"to"` // optional: defaults to the captured user
	FilePath     string `json:"file_path"`
	Caption      string `json:"caption"`
	ContextToken string `json:"context_token"`
}

func (h *handler) sendImage(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req mediaReq
	if !decode(w, r, &req) || !requireMedia(w, req.FilePath) {
		return
	}
	clientID, err := h.sender.SendImage(r.Context(), req.To, req.FilePath, req.Caption, req.ContextToken)
	h.respond(w, clientID, err)
}

func (h *handler) sendVideo(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req mediaReq
	if !decode(w, r, &req) || !requireMedia(w, req.FilePath) {
		return
	}
	clientID, err := h.sender.SendVideo(r.Context(), req.To, req.FilePath, req.Caption, req.ContextToken)
	h.respond(w, clientID, err)
}

func (h *handler) sendFile(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req mediaReq
	if !decode(w, r, &req) || !requireMedia(w, req.FilePath) {
		return
	}
	clientID, err := h.sender.SendFile(r.Context(), req.To, req.FilePath, req.Caption, req.ContextToken)
	h.respond(w, clientID, err)
}

func (h *handler) sendMedia(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req mediaReq
	if !decode(w, r, &req) || !requireMedia(w, req.FilePath) {
		return
	}
	err := h.sender.SendMedia(r.Context(), req.To, req.FilePath, req.Caption, req.ContextToken)
	h.respond(w, "", err)
}

type typingReq struct {
	To     string `json:"to"` // optional: defaults to the captured user
	Ticket string `json:"ticket"`
	On     bool   `json:"on"`
}

func (h *handler) sendTyping(w http.ResponseWriter, r *http.Request) {
	if !h.checkSession(w) {
		return
	}
	var req typingReq
	if !decode(w, r, &req) {
		return
	}
	if req.Ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ticket is required"})
		return
	}
	err := h.sender.SendTyping(r.Context(), req.To, req.Ticket, req.On)
	h.respond(w, "", err)
}

func (h *handler) respond(w http.ResponseWriter, clientID string, err error) {
	if err != nil {
		h.log.Printf("[api] send failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	body := map[string]any{"ok": true}
	if clientID != "" {
		body["client_id"] = clientID
	}
	writeJSON(w, http.StatusOK, body)
}

func requireMedia(w http.ResponseWriter, filePath string) bool {
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "file_path is required"})
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	defer func() { _ = r.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(out); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json: " + err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
