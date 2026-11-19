package fsx

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"
)

// Block transfer (docs/requirements.md §5.6): files from BlockMin up are
// split into fixed-size blocks, each with its own hash. A receiver copies
// the blocks it already has from its old version and fetches only the rest.
const (
	BlockMin      = 1 << 20 // smaller files are sent whole
	blockSmallest = 128 << 10
	blockLargest  = 16 << 20
	blocksTarget  = 2000 // aim for about this many blocks per file
)

// BlockSize returns the block size for a file of size n (0 = no blocks).
func BlockSize(n int64) int {
	if n < BlockMin {
		return 0
	}
	bs := int64(blockSmallest)
	for n/bs > blocksTarget && bs < blockLargest {
		bs *= 2
	}
	return int(bs)
}

// Hashes is a file's whole-content hash and, for large files, its block hashes.
type Hashes struct {
	Hash      string
	BlockSize int
	Blocks    []string // 128-bit BLAKE3 per block, hex
}

func blockHash(b []byte) string {
	h := blake3.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// HashFileBlocks hashes a file in one pass: the whole file and its blocks.
func HashFileBlocks(p string) (Hashes, error) {
	f, err := os.Open(p)
	if err != nil {
		return Hashes{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Hashes{}, err
	}
	whole := blake3.New(32, nil)
	res := Hashes{BlockSize: BlockSize(fi.Size())}
	if res.BlockSize == 0 {
		if _, err := io.Copy(whole, f); err != nil {
			return Hashes{}, err
		}
		res.Hash = hex.EncodeToString(whole.Sum(nil))
		return res, nil
	}
	buf := make([]byte, res.BlockSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			whole.Write(buf[:n])
			res.Blocks = append(res.Blocks, blockHash(buf[:n]))
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return Hashes{}, err
		}
	}
	res.Hash = hex.EncodeToString(whole.Sum(nil))
	return res, nil
}

// BlockLen is the length of block i of a file.
func BlockLen(size int64, bs, i int) int64 {
	return min(int64(bs), size-int64(i)*int64(bs))
}

// Plan says which blocks of a new version can be copied from an old one.
// Have maps a block hash to its offset in the old file.
func Plan(oldBlocks []string, bs int) map[string]int64 {
	have := make(map[string]int64, len(oldBlocks))
	for i, h := range oldBlocks {
		if _, dup := have[h]; !dup {
			have[h] = int64(i) * int64(bs)
		}
	}
	return have
}

// Missing lists the blocks of newBlocks that have no copy in have.
func Missing(newBlocks []string, have map[string]int64) []int {
	out := []int{}
	for i, h := range newBlocks {
		if _, ok := have[h]; !ok {
			out = append(out, i)
		}
	}
	return out
}

// AssembleStats counts where the bytes of an assembled file came from.
type AssembleStats struct {
	Reused, Fetched int64
}

// Assemble writes a new version of root/rel into a temp file (like
// Receive), block by block: block i is copied from old at the offset
// local(i, hash) returns, or else read from fetch(i, n), which delivers the
// fetched blocks in increasing order. The whole-file hash is checked at the end.
func Assemble(stage, root, rel string, size int64, bs int, blocks []string, wantHash string,
	old *os.File, local func(i int, hash string) (int64, bool), fetch func(i int, n int64) (io.Reader, error)) (string, AssembleStats, error) {
	var st AssembleStats
	dst := Abs(root, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", st, err
	}
	dir := stage
	if dir == "" {
		dir = filepath.Dir(dst)
	}
	tmp := filepath.Join(dir, randName())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", st, err
	}
	fail := func(err error) (string, AssembleStats, error) {
		f.Close()
		os.Remove(tmp)
		return "", st, err
	}
	h := blake3.New(32, nil)
	w := io.MultiWriter(f, h)
	for i, bh := range blocks {
		n := BlockLen(size, bs, i)
		if off, ok := local(i, bh); ok && old != nil {
			if _, err := io.Copy(w, io.NewSectionReader(old, off, n)); err != nil {
				return fail(err)
			}
			st.Reused += n
			continue
		}
		r, err := fetch(i, n)
		if err != nil {
			return fail(err)
		}
		got, err := io.CopyN(w, r, n)
		if err != nil {
			return fail(fmt.Errorf("block %d: %w", i, err))
		}
		st.Fetched += got
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHash {
		return fail(fmt.Errorf("%s: assembled content hash %s, want %s", rel, short(got), short(wantHash)))
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", st, err
	}
	return tmp, st, nil
}

// EncodeIndexes writes block indexes compactly: "0-4,9,12-20".
func EncodeIndexes(ix []int) string {
	var b []byte
	for i := 0; i < len(ix); {
		j := i
		for j+1 < len(ix) && ix[j+1] == ix[j]+1 {
			j++
		}
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%d", ix[i])
		if j > i {
			b = fmt.Appendf(b, "-%d", ix[j])
		}
		i = j + 1
	}
	return string(b)
}

// DecodeIndexes reads EncodeIndexes' format; at most max indexes, each below limit.
func DecodeIndexes(s string, limit int) (map[int]bool, error) {
	out := map[int]bool{}
	if s == "" {
		return out, nil
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != ',' {
			continue
		}
		part := s[start:i]
		start = i + 1
		var a, b int
		if n, _ := fmt.Sscanf(part, "%d-%d", &a, &b); n == 2 {
		} else if n, _ := fmt.Sscanf(part, "%d", &a); n == 1 {
			b = a
		} else {
			return nil, fmt.Errorf("bad block list %q", part)
		}
		if a < 0 || b < a || b >= limit {
			return nil, fmt.Errorf("block range %q out of bounds", part)
		}
		for k := a; k <= b; k++ {
			out[k] = true
		}
	}
	return out, nil
}
