// Package sender wraps the ilink SDK client with context-token resolution so both
// the CLI and the HTTP API can send every ilink message type through one code path.
package sender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ilink "github.com/openilink/openilink-sdk-go"

	"github.com/phpgao/ilinkgo/internal/store"
)

// Sender sends messages on behalf of the logged-in bot.
type Sender struct {
	Client *ilink.Client
	Store  *store.Store
}

// New creates a Sender.
func New(client *ilink.Client, st *store.Store) *Sender {
	return &Sender{Client: client, Store: st}
}

// resolveRecipient returns the recipient to send to. An explicit one (--to / "to" in the
// API) always wins; otherwise fall back to the default user captured while monitoring.
// Most setups only ever talk to that one user, so --to can be omitted.
func (s *Sender) resolveRecipient(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if s.Store != nil {
		if u := s.Store.LoadDefaultUser(); u != "" {
			return u, nil
		}
	}
	return "", fmt.Errorf("no recipient: pass --to (or 'to' in the API), or run 'ilinkgo serve' and message the bot first")
}

// resolve returns the recipient and context token to use for an outbound message.
func (s *Sender) resolve(to, contextToken string) (string, string, error) {
	to, err := s.resolveRecipient(to)
	if err != nil {
		return "", "", err
	}
	token, err := s.resolveToken(to, contextToken)
	if err != nil {
		return "", "", err
	}
	return to, token, nil
}

// resolveToken picks the context token to use, in order: an explicit token (e.g. from
// an inbound message the caller just handled), the SDK's in-memory cache (populated by
// Monitor while `serve` is running), then the on-disk cache (populated by `serve` across
// restarts). Weixin's iLink protocol requires a context token from a prior inbound
// message before the bot may push to that user.
func (s *Sender) resolveToken(to, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if t, ok := s.Client.GetContextToken(to); ok && t != "" {
		return t, nil
	}
	if s.Store != nil {
		if t, ok := s.Store.LoadContextToken(to); ok {
			s.Client.SetContextToken(to, t)
			return t, nil
		}
	}
	return "", fmt.Errorf("no context token cached for user %q: pass an explicit context token, or run 'ilinkgo serve' and wait for the user to message the bot first", to)
}

// SendText sends a plain text message and returns the generated client_id.
func (s *Sender) SendText(ctx context.Context, to, text, contextToken string) (string, error) {
	to, token, err := s.resolve(to, contextToken)
	if err != nil {
		return "", err
	}
	return s.Client.SendText(ctx, to, text, token)
}

// SendImage uploads a local image file and sends it.
func (s *Sender) SendImage(ctx context.Context, to, filePath, caption, contextToken string) (string, error) {
	return s.sendUploaded(ctx, to, filePath, caption, contextToken, ilink.MediaImage, s.Client.SendImage)
}

// SendVideo uploads a local video file and sends it.
func (s *Sender) SendVideo(ctx context.Context, to, filePath, caption, contextToken string) (string, error) {
	return s.sendUploaded(ctx, to, filePath, caption, contextToken, ilink.MediaVideo, s.Client.SendVideo)
}

// SendFile uploads a local file and sends it as a generic file attachment.
func (s *Sender) SendFile(ctx context.Context, to, filePath, caption, contextToken string) (string, error) {
	to, token, err := s.resolve(to, contextToken)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	uploaded, err := s.Client.UploadFile(ctx, data, to, ilink.MediaFile)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	if err := s.sendCaption(ctx, to, caption, token); err != nil {
		return "", err
	}
	return s.Client.SendFileAttachment(ctx, to, token, filepath.Base(filePath), uploaded)
}

// SendMedia auto-detects the media type from the file's extension (image/video/file)
// and sends it, mirroring the SDK's high-level SendMediaFile helper.
func (s *Sender) SendMedia(ctx context.Context, to, filePath, caption, contextToken string) error {
	to, token, err := s.resolve(to, contextToken)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	return s.Client.SendMediaFile(ctx, to, token, data, filepath.Base(filePath), caption)
}

// SendTyping sends or cancels the typing indicator for a user.
func (s *Sender) SendTyping(ctx context.Context, to, ticket string, on bool) error {
	status := ilink.CancelTyping
	if on {
		status = ilink.Typing
	}
	return s.Client.SendTyping(ctx, to, ticket, status)
}

func (s *Sender) sendCaption(ctx context.Context, to, caption, token string) error {
	if caption == "" {
		return nil
	}
	if _, err := s.Client.SendText(ctx, to, caption, token); err != nil {
		return fmt.Errorf("send caption: %w", err)
	}
	return nil
}

func (s *Sender) sendUploaded(
	ctx context.Context, to, filePath, caption, contextToken string,
	mediaType ilink.UploadMediaType,
	send func(ctx context.Context, to, contextToken string, uploaded *ilink.UploadResult) (string, error),
) (string, error) {
	to, token, err := s.resolve(to, contextToken)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	uploaded, err := s.Client.UploadFile(ctx, data, to, mediaType)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	if err := s.sendCaption(ctx, to, caption, token); err != nil {
		return "", err
	}
	return send(ctx, to, token, uploaded)
}
