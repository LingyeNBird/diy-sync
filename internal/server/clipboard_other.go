//go:build !windows

package server

import (
	"context"
	"log/slog"
	"sync"

	"fdsync/internal/protocol"
)

type winClipboardBridge struct {
	mu       sync.Mutex
	lastText string // stored in memory for testing on Linux
}

func newWinClipboardBridge() *winClipboardBridge { return &winClipboardBridge{} }

func (b *winClipboardBridge) start(_ context.Context, _ *clipboardStore, _ *slog.Logger) {}

func (b *winClipboardBridge) onWSLImage(_ protocol.ClipboardItem, _ []byte, _ bool, _ *slog.Logger) {}

func (b *winClipboardBridge) onWSLText(text string, _ *slog.Logger) error {
	b.mu.Lock()
	b.lastText = text
	b.mu.Unlock()
	return nil
}
