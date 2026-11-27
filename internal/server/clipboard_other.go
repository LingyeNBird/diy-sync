//go:build !windows

package server

import (
	"context"
	"log/slog"
)

// startClipboardWatch is a no-op outside Windows (images can still be uploaded
// via the panel API for testing or manual use).
func startClipboardWatch(_ context.Context, _ *clipboardStore, _ *slog.Logger) {}
