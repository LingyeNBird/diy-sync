//go:build !windows

package server

import (
	"context"
	"log/slog"
)

// HandleToastActivation is a Windows-only entry point for toast clicks.
func HandleToastActivation(_ context.Context, _ *slog.Logger) {}
