package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"fdsync/internal/protocol"
)

// ClipboardSource is satisfied both by a direct *Client talking to
// fdsync-server and by *LocalIPCClient talking to a running local daemon.
type ClipboardSource interface {
	ClipboardList(ctx context.Context) (*protocol.ClipboardList, error)
	ClipboardImage(ctx context.Context, id string) ([]byte, error)
}

// Ping verifies that the server is reachable and this client's credentials are
// currently valid.
func (c *Client) Ping(ctx context.Context) error {
	err := c.do(ctx, "GET", "/folders", nil, nil, nil)
	if isUnauthorized(err) && c.Tokens != nil {
		err = c.do(ctx, "GET", "/folders", nil, nil, nil)
	}
	return err
}

// ClipboardList returns the server's current clipboard image list.
func (c *Client) ClipboardList(ctx context.Context) (*protocol.ClipboardList, error) {
	var out protocol.ClipboardList
	err := c.do(ctx, "GET", "/clipboard", nil, nil, &out)
	if isUnauthorized(err) && c.Tokens != nil {
		// Server may have restarted and dropped in-memory access tokens; retry once.
		err = c.do(ctx, "GET", "/clipboard", nil, nil, &out)
	}
	if err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []protocol.ClipboardItem{}
	}
	return &out, nil
}

// ClipboardImage downloads one PNG image by ID from the server.
func (c *Client) ClipboardImage(ctx context.Context, id string) ([]byte, error) {
	b, err := c.clipboardImageOnce(ctx, id)
	if isUnauthorized(err) && c.Tokens != nil {
		b, err = c.clipboardImageOnce(ctx, id)
	}
	return b, err
}

func (c *Client) clipboardImageOnce(ctx context.Context, id string) ([]byte, error) {
	u := c.BaseURL + protocol.APIPrefix + "/clipboard/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && c.Tokens != nil {
		c.Tokens.Invalidate()
	}
	if resp.StatusCode >= 300 {
		var e protocol.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return nil, &httpError{resp.StatusCode, e.Error}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

func isUnauthorized(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.code == http.StatusUnauthorized
}

// ---------------------------------------------------------------- local daemon IPC

// ServeLocalIPC exposes a local-only Unix domain socket (mode 0600) so CLI
// subcommands such as `fdsync clip` can reuse the running daemon's HTTPS
// connection and rotating TokenSource without racing on credentials.json.
func ServeLocalIPC(ctx context.Context, sockPath string, c *Client, log *slog.Logger) error {
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(sockPath)
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		pctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := c.Ping(pctx); err != nil {
			writeLocalErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /clipboard", func(w http.ResponseWriter, r *http.Request) {
		list, err := c.ClipboardList(r.Context())
		if err != nil {
			writeLocalErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("GET /clipboard/{id}", func(w http.ResponseWriter, r *http.Request) {
		b, err := c.ClipboardImage(r.Context(), r.PathValue("id"))
		if err != nil {
			writeLocalErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	defer os.Remove(sockPath)

	if log != nil {
		log.Debug("local ipc listening", "socket", sockPath)
	}
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func writeLocalErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var he *httpError
	if errors.As(err, &he) && he.code >= 400 && he.code < 600 {
		code = he.code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(protocol.Error{Error: err.Error()})
}

// LocalIPCClient talks to a running daemon over its local Unix domain socket.
type LocalIPCClient struct {
	hc *http.Client
}

// DialLocalIPC checks whether a daemon is listening on sockPath and returns a
// client bound to that socket.
func DialLocalIPC(ctx context.Context, sockPath string) (*LocalIPCClient, error) {
	var d net.Dialer
	d.Timeout = 500 * time.Millisecond
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, err
	}
	_ = conn.Close()

	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", sockPath)
		},
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &LocalIPCClient{hc: &http.Client{Transport: tr}}, nil
}

func (l *LocalIPCClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://fdsync.local/ping", nil)
	if err != nil {
		return err
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeLocalErr(resp)
	}
	return nil
}

func (l *LocalIPCClient) ClipboardList(ctx context.Context) (*protocol.ClipboardList, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://fdsync.local/clipboard", nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeLocalErr(resp)
	}
	var out protocol.ClipboardList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []protocol.ClipboardItem{}
	}
	return &out, nil
}

func (l *LocalIPCClient) ClipboardImage(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://fdsync.local/clipboard/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeLocalErr(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

func decodeLocalErr(resp *http.Response) error {
	var e protocol.Error
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("daemon ipc: %s", resp.Status)
}
