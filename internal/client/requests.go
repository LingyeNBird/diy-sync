package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/protocol"
)

// ServeRequests answers what the server asks of this client on behalf of
// the panel: a file's content (for preview and diff, never stored on
// Windows) or a delete on this side. It long-polls until ctx ends.
func (c *Client) ServeRequests(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		var reqs []protocol.Request
		err := c.do(ctx, "GET", "/requests", map[string][]string{"wait": {"30"}}, nil, &reqs)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, r := range reqs {
			go c.answer(ctx, r)
		}
	}
}

func (c *Client) session(folder string) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s.b.Folder == folder {
			return s
		}
	}
	return nil
}

func (c *Client) answer(ctx context.Context, r protocol.Request) {
	var body []byte
	var size int64
	err := func() error {
		switch r.Kind {
		case protocol.ReqList:
			var err error
			body, err = c.list(r.Path)
			return err
		case protocol.ReqBind:
			return c.bind(r.Folder, r.Path)
		case protocol.ReqTrash:
			var err error
			body, err = c.trashList()
			return err
		}
		s := c.session(r.Folder)
		if s == nil {
			return fmt.Errorf("no binding for folder %q", r.Folder)
		}
		switch r.Kind {
		case protocol.ReqRead:
			if r.Trash != "" {
				var err error
				body, size, err = s.readTrash(r.Trash, r.Offset, r.Limit)
				return err
			}
		case protocol.ReqRestore:
			return s.restore(r.Trash)
		}
		if err := fsx.CheckRel(r.Path); err != nil {
			return err
		}
		switch r.Kind {
		case protocol.ReqRead:
			var err error
			body, size, err = s.read(r.Path, r.Offset, r.Limit)
			return err
		case protocol.ReqDelete:
			return s.deletePrivate(ctx, r.Path)
		}
		return fmt.Errorf("unknown request %q", r.Kind)
	}()
	req, _ := http.NewRequestWithContext(ctx, "PUT", c.getBaseURL()+protocol.APIPrefix+"/requests/"+r.ID, bytes.NewReader(body))
	if err := c.authorize(ctx, req); err != nil {
		return
	}
	req.Header.Set(protocol.HeaderSize, strconv.FormatInt(size, 10))
	if err != nil {
		req.Header.Set(protocol.HeaderError, err.Error())
	}
	if resp, err := c.HTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

// allowed resolves an absolute local path the panel sent and checks that it
// lies under c.Root.
func (c *Client) allowed(p string) (string, error) {
	root := c.Root
	if root == "" {
		root, _ = os.UserHomeDir()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if p == "" {
		p = root
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	p = filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", p, root)
	}
	return p, nil
}

func (c *Client) list(p string) ([]byte, error) {
	p, err := c.allowed(p)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	home, _ := c.allowed("")
	res := protocol.ListResult{Path: p, Home: home, Dirs: []string{}}
	for _, e := range ents {
		if e.IsDir() { // not symlinks to folders
			res.Dirs = append(res.Dirs, e.Name())
		} else {
			res.Files++
		}
	}
	sort.Strings(res.Dirs)
	return json.Marshal(res)
}

func (c *Client) bind(folder, p string) error {
	if c.OnBind == nil {
		return errors.New("this client doesn't accept bindings from the panel")
	}
	p, err := c.allowed(p)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", p)
	}
	c.mu.Lock()
	for _, s := range c.sessions {
		if s.b.Folder == folder {
			c.mu.Unlock()
			return fmt.Errorf("folder %q is already bound here (%s)", folder, s.b.Root)
		}
	}
	c.mu.Unlock()
	return c.OnBind(Binding{Folder: folder, Root: p})
}

// read returns up to limit bytes of a regular file (symlinks are not followed).
func (s *Session) read(rel string, offset, limit int64) ([]byte, int64, error) {
	p := fsx.Abs(s.b.Root, rel)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, errors.New("not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	return readPart(f, offset, limit)
}

// readTrash reads a kept version from this binding's trash.
func (s *Session) readTrash(id string, offset, limit int64) ([]byte, int64, error) {
	f, err := s.side.Trash.Open(id)
	if err != nil {
		return nil, 0, err
	}
	return readPart(f, offset, limit)
}

// restore puts a kept version back; the watcher sees it and it syncs.
func (s *Session) restore(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, err := s.side.Trash.Restore(id, s.side.Stage, s.b.Root)
	if err == nil {
		s.c.Log.Info("restored from the trash (panel)", "folder", s.b.Folder, "path", rel)
	}
	return err
}

func (c *Client) trashList() ([]byte, error) {
	c.mu.Lock()
	sessions := append([]*Session(nil), c.sessions...)
	c.mu.Unlock()
	out := []protocol.TrashList{}
	for _, s := range sessions {
		out = append(out, protocol.TrashList{Folder: s.b.Folder, Items: s.side.Trash.List()})
	}
	return json.Marshal(out)
}

func readPart(f *os.File, offset, limit int64) ([]byte, int64, error) {
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 16 << 20
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, 0, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, limit))
	return b, fi.Size(), err
}

// deletePrivate deletes a path on this side (an admin action from the
// panel). It must be outside the scope by then, so the delete can't sync
// to Windows: the server cancels synchronization first, and we check the
// current scope ourselves before touching anything.
func (s *Session) deletePrivate(ctx context.Context, rel string) error {
	s.mu.Lock() // no sync round in between
	defer s.mu.Unlock()
	if _, err := s.refreshScope(ctx); err != nil {
		return err
	}
	if s.scope.InScope(rel, false) || s.scope.InScope(rel, true) {
		return fmt.Errorf("%s is still in scope", rel)
	}
	abs := fsx.Abs(s.b.Root, rel)
	if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_ = filepath.WalkDir(abs, func(q string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			r, _ := filepath.Rel(s.b.Root, q)
			_ = s.side.Trash.Put(s.b.Root, filepath.ToSlash(r))
		}
		return nil
	})
	return os.RemoveAll(abs)
}
