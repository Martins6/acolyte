package traceimport

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Extraction size caps. They are vars (not consts) so tests can shrink them.
var (
	maxFileBytes  int64 = 64 << 20  // per-entry uncompressed cap
	maxTotalBytes int64 = 512 << 20 // per-bundle uncompressed cap
)

// traceBundle is an opened, validated trace zip.
type traceBundle struct {
	reader   *zip.Reader
	closer   io.Closer
	manifest *manifest
}

// openTrace opens zipPath, reads its manifest, and validates the format.
func openTrace(zipPath string) (*traceBundle, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("traceimport: open zip: %w", err)
	}
	bundle := &traceBundle{reader: &reader.Reader, closer: reader}
	if bundle.locate("manifest.json") == nil {
		bundle.Close()
		return nil, fmt.Errorf("traceimport: %s has no manifest.json (not an acolyte memory trace)", zipPath)
	}
	raw, err := bundle.readEntry("manifest.json")
	if err != nil {
		bundle.Close()
		return nil, err
	}
	m, err := parseManifest(raw)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	bundle.manifest = m
	return bundle, nil
}

// Close releases the zip file handle.
func (b *traceBundle) Close() error {
	if b.closer != nil {
		return b.closer.Close()
	}
	return nil
}

// locate returns the entry whose normalized name matches name, or nil.
func (b *traceBundle) locate(name string) *zip.File {
	for _, f := range b.reader.File {
		clean, err := safeEntryName(f.Name)
		if err != nil {
			continue
		}
		if clean == name {
			return f
		}
	}
	return nil
}

// entry validates an untrusted entry name (e.g. a manifest-provided path)
// and returns its cleaned form.
func (b *traceBundle) entry(name string) (string, error) {
	return safeEntryName(name)
}

// measureEntry streams one regular entry, enforcing the per-file cap on the
// bytes actually decompressed (headers can lie) and surfacing read/CRC
// errors. It returns the measured byte count.
func measureEntry(f *zip.File) (int64, error) {
	if declared := int64(f.UncompressedSize64); declared > maxFileBytes {
		return 0, fmt.Errorf("traceimport: zip entry %q declares %d bytes (limit %d)", f.Name, declared, maxFileBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("traceimport: open zip entry %q: %w", f.Name, err)
	}
	n, copyErr := io.Copy(io.Discard, io.LimitReader(rc, maxFileBytes+1))
	rc.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("traceimport: measure zip entry %q: %w", f.Name, copyErr)
	}
	if n > maxFileBytes {
		return 0, fmt.Errorf("traceimport: zip entry %q exceeds %d bytes", f.Name, maxFileBytes)
	}
	return n, nil
}

// requirePayloadBudget streams every named entry to count its real
// decompressed size, enforcing the per-file cap on actual bytes (headers can
// lie) and the aggregate cap across the set. It runs before any import
// request — dry-run included — so an over-limit bundle is rejected without
// promising or issuing imports.
func (b *traceBundle) requirePayloadBudget(names []string) error {
	var total int64
	for _, name := range names {
		clean, err := safeEntryName(name)
		if err != nil {
			return err
		}
		f := b.locate(clean)
		if f == nil {
			return fmt.Errorf("traceimport: zip entry %q not found", name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("traceimport: refusing symlink zip entry %q", clean)
		}
		n, err := measureEntry(f)
		if err != nil {
			return err
		}
		total += n
		if total > maxTotalBytes {
			return fmt.Errorf("traceimport: session payloads exceed %d bytes uncompressed (limit %d)", total, maxTotalBytes)
		}
	}
	return nil
}

// readEntry reads one entry fully, enforcing the per-file size cap. The name
// is validated first because manifest-provided paths are untrusted input.
func (b *traceBundle) readEntry(name string) ([]byte, error) {
	clean, err := b.entry(name)
	if err != nil {
		return nil, err
	}
	f := b.locate(clean)
	if f == nil {
		return nil, fmt.Errorf("traceimport: zip entry %q not found", name)
	}
	if f.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("traceimport: refusing symlink zip entry %q", clean)
	}
	if declared := int64(f.UncompressedSize64); declared > maxFileBytes {
		return nil, fmt.Errorf("traceimport: zip entry %q declares %d bytes (limit %d)", clean, declared, maxFileBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("traceimport: open zip entry %q: %w", clean, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("traceimport: read zip entry %q: %w", clean, err)
	}
	if int64(len(raw)) > maxFileBytes {
		return nil, fmt.Errorf("traceimport: zip entry %q exceeds %d bytes", clean, maxFileBytes)
	}
	return raw, nil
}

// validate walks every entry and enforces the structural and size limits
// without writing anything. Every regular entry is streamed so the per-file
// and aggregate caps apply to the bytes actually decompressed (headers can
// lie) and read/CRC errors surface here instead of during extraction. It
// returns the number of file entries.
func (b *traceBundle) validate() (int, error) {
	var total int64
	files := 0
	for _, f := range b.reader.File {
		if _, _, err := checkEntry(f); err != nil {
			return 0, err
		}
		if entryIsDir(f) {
			continue
		}
		n, err := measureEntry(f)
		if err != nil {
			return 0, err
		}
		total += n
		if total > maxTotalBytes {
			return 0, fmt.Errorf("traceimport: trace exceeds %d bytes uncompressed (limit %d)", total, maxTotalBytes)
		}
		files++
	}
	return files, nil
}

// safeEntryName validates an untrusted zip entry name and returns the cleaned
// slash-separated path. It rejects absolute paths (including Windows drive
// notation and backslash traversal), and any ".." segment.
func safeEntryName(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("traceimport: empty zip entry name")
	}
	// Zip names use forward slashes; treat backslashes as separators too so
	// Windows-style traversal (..\..\x) cannot slip through on unix.
	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("traceimport: absolute zip entry path %q", name)
	}
	if len(normalized) >= 2 && normalized[1] == ':' && isASCIILetter(normalized[0]) {
		return "", fmt.Errorf("traceimport: absolute zip entry path %q", name)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", fmt.Errorf("traceimport: zip entry %q escapes the trace root", name)
		}
	}
	clean := path.Clean(normalized)
	if clean == "." || clean == "/" {
		return "", fmt.Errorf("traceimport: invalid zip entry path %q", name)
	}
	return clean, nil
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// entryIsDir reports whether a zip entry is a directory.
func entryIsDir(f *zip.File) bool {
	return strings.HasSuffix(f.Name, "/") || f.Mode().IsDir()
}

// checkEntry validates one entry's name and file type: symlinks and special
// files (devices, sockets, ...) are rejected outright.
func checkEntry(f *zip.File) (clean string, isDir bool, err error) {
	clean, err = safeEntryName(f.Name)
	if err != nil {
		return "", false, err
	}
	mode := f.Mode()
	if mode&fs.ModeSymlink != 0 {
		return "", false, fmt.Errorf("traceimport: refusing symlink zip entry %q", f.Name)
	}
	isDir = entryIsDir(f)
	if !isDir && !mode.IsRegular() {
		return "", false, fmt.Errorf("traceimport: refusing non-regular zip entry %q", f.Name)
	}
	return clean, isDir, nil
}

// checkDestination reports whether dest can be published: existing trace
// folders require --force, and symlinked destinations are refused outright.
func checkDestination(dest string, force bool) error {
	info, err := os.Lstat(dest)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("traceimport: stat %s: %w", dest, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("traceimport: refusing symlinked trace folder %s", dest)
	}
	if !force {
		return fmt.Errorf("traceimport: %s already exists (use --force to replace)", dest)
	}
	return nil
}

// rejectSymlinkAncestors walks each path component from root down to target
// with Lstat and rejects symlinked or non-directory components. Missing
// components are allowed: they are created later. It also rejects a target
// that escapes root. This is what keeps MkdirAll/MkdirTemp from following a
// pre-existing symlink (e.g. a planted downloads/ link) out of the workspace.
func rejectSymlinkAncestors(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("traceimport: resolve %s relative to %s: %w", target, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("traceimport: %s is outside %s", target, root)
	}
	current := root
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil // remaining components do not exist yet; they will be created
		}
		if err != nil {
			return fmt.Errorf("traceimport: stat %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("traceimport: refusing symlinked path component %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("traceimport: %s is not a directory", current)
		}
	}
	return nil
}

// extractTrace safely extracts reader into dest. Files are written into a
// temporary directory next to dest and atomically renamed into place, so a
// failed extraction never leaves a partial trace folder behind. workspace is
// the root the destination must stay beneath.
func extractTrace(workspace string, reader *zip.Reader, dest string, force bool) (int, error) {
	parent := filepath.Dir(dest)
	if err := rejectSymlinkAncestors(workspace, parent); err != nil {
		return 0, err
	}
	if err := checkDestination(dest, force); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return 0, fmt.Errorf("traceimport: create trace directory: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-")
	if err != nil {
		return 0, fmt.Errorf("traceimport: create temporary directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(tmp)
		}
	}()

	var total int64
	files := 0
	for _, f := range reader.File {
		clean, isDir, err := checkEntry(f)
		if err != nil {
			return 0, err
		}
		target := filepath.Join(tmp, filepath.FromSlash(clean))
		rel, err := filepath.Rel(tmp, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return 0, fmt.Errorf("traceimport: zip entry %q escapes the trace root", f.Name)
		}
		if isDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, fmt.Errorf("traceimport: create directory %q: %w", clean, err)
			}
			continue
		}
		if declared := int64(f.UncompressedSize64); declared > maxFileBytes {
			return 0, fmt.Errorf("traceimport: zip entry %q declares %d bytes (limit %d)", clean, declared, maxFileBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, fmt.Errorf("traceimport: create parent directory for %q: %w", clean, err)
		}
		n, err := writeEntry(f, target)
		if err != nil {
			return 0, err
		}
		total += n
		if total > maxTotalBytes {
			return 0, fmt.Errorf("traceimport: trace exceeds %d bytes uncompressed (limit %d)", total, maxTotalBytes)
		}
		files++
	}

	if err := publishTrace(workspace, tmp, dest, force); err != nil {
		return 0, err
	}
	published = true
	return files, nil
}

// writeEntry copies one file entry to target, enforcing the per-file cap on
// the bytes actually decompressed (headers can lie). It returns the bytes
// written. The entry's exec bits are preserved; everything else is 0644.
func writeEntry(f *zip.File, target string) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("traceimport: open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()
	mode := os.FileMode(0o644) | (f.Mode() & 0o111)
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("traceimport: create %q: %w", target, err)
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, maxFileBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("traceimport: extract %q: %w", f.Name, copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("traceimport: write %q: %w", target, closeErr)
	}
	if n > maxFileBytes {
		return 0, fmt.Errorf("traceimport: zip entry %q exceeds %d bytes", f.Name, maxFileBytes)
	}
	return n, nil
}

// publishTrace atomically moves the extracted tree at tmp to dest. An existing
// destination (only reachable with force) is moved aside first and removed
// after the swap succeeds, so a failure restores the previous folder.
// workspace anchors the ancestor check so neither the backup temp name nor
// the final rename can operate through a symlinked parent.
func publishTrace(workspace, tmp, dest string, force bool) error {
	parent := filepath.Dir(dest)
	if err := rejectSymlinkAncestors(workspace, parent); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		if !force {
			return fmt.Errorf("traceimport: %s already exists (use --force to replace)", dest)
		}
		backup, err := reserveTempName(parent, ".old-")
		if err != nil {
			return err
		}
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("traceimport: move existing trace aside: %w", err)
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Rename(backup, dest) // best-effort restore
			return fmt.Errorf("traceimport: finalize trace: %w", err)
		}
		os.RemoveAll(backup)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("traceimport: stat %s: %w", dest, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("traceimport: finalize trace: %w", err)
	}
	return nil
}

// reserveTempName returns a unique unused path in dir by creating and
// removing a temporary entry (only the name is needed).
func reserveTempName(dir, pattern string) (string, error) {
	path, err := os.MkdirTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("traceimport: reserve temporary name: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("traceimport: reserve temporary name: %w", err)
	}
	return path, nil
}
