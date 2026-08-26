package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

const LedgerName = "ledger.jsonl"

type Entry struct {
	RelativePath string    `json:"relative_path"`
	SHA256       string    `json:"sha256"`
	Bytes        int64     `json:"bytes"`
	RecordedAt   time.Time `json:"recorded_at"`
}

func Record(root, relativePath string) (Entry, error) {
	path, normalized, err := safeArtifactPath(root, relativePath)
	if err != nil {
		return Entry{}, err
	}
	existing, err := readLedger(root)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range existing {
		if entry.RelativePath == normalized {
			return Entry{}, fmt.Errorf("artifact %q is already recorded", normalized)
		}
	}
	hash, size, err := hashFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("hash artifact %q: %w", normalized, err)
	}
	entry := Entry{
		RelativePath: normalized,
		SHA256:       hash,
		Bytes:        size,
		RecordedAt:   time.Now().UTC(),
	}
	if err := jsonl.Append(filepath.Join(root, LedgerName), entry); err != nil {
		return Entry{}, fmt.Errorf("append artifact ledger: %w", err)
	}
	return entry, nil
}

func Verify(root string) error {
	entries, err := readLedger(root)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if _, exists := seen[entry.RelativePath]; exists {
			return fmt.Errorf("ledger entry %d repeats relative path %q", index+1, entry.RelativePath)
		}
		seen[entry.RelativePath] = struct{}{}
		path, normalized, err := safeArtifactPath(root, entry.RelativePath)
		if err != nil {
			return fmt.Errorf("ledger entry %d: %w", index+1, err)
		}
		if normalized != entry.RelativePath {
			return fmt.Errorf("ledger entry %d has non-canonical relative path %q", index+1, entry.RelativePath)
		}
		hash, size, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("verify artifact %q: %w", entry.RelativePath, err)
		}
		if hash != entry.SHA256 {
			return fmt.Errorf("artifact %q sha256 mismatch: got %s, want %s", entry.RelativePath, hash, entry.SHA256)
		}
		if size != entry.Bytes {
			return fmt.Errorf("artifact %q size mismatch: got %d, want %d", entry.RelativePath, size, entry.Bytes)
		}
	}
	return nil
}

func readLedger(root string) ([]Entry, error) {
	path := filepath.Join(root, LedgerName)
	entries, err := jsonl.Read[Entry](path)
	if os.IsNotExist(unwrapPathError(err)) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read artifact ledger: %w", err)
	}
	return entries, nil
}

func unwrapPathError(err error) error {
	for err != nil {
		pathError, ok := err.(*os.PathError)
		if ok {
			return pathError
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := err.(unwrapper)
		if !ok {
			return err
		}
		err = next.Unwrap()
	}
	return nil
}

func safeArtifactPath(root, relativePath string) (string, string, error) {
	if strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) {
		return "", "", fmt.Errorf("artifact relative path must be non-empty and relative")
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativePath)))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", "", fmt.Errorf("artifact relative path %q escapes root", relativePath)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact root symlinks: %w", err)
	}
	path := filepath.Join(rootResolved, filepath.FromSlash(normalized))
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact relative path %q: %w", relativePath, err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact relative path %q escapes root", relativePath)
	}
	return resolved, normalized, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a regular file")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}
