package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Large-envelope ciphertext is stored in files under BlobDir instead of inline
// in SQLite (DEV_SPEC §3.2). All blob paths are server-generated from validated
// ids (bridge_id, sender_land_id, msg_id), never from client-supplied strings,
// so there is no path-traversal surface (DEV_SPEC §16). Files are 0600 and
// directories 0700.
//
// Deviation (DEV_SPEC §19): the spec sketches the path as
// blobs/<bridge_id>/<msg_id>, but msg_id is only unique per sender, so two
// senders sharing a msg_id would collide. The path includes sender_land_id.

// blobPathFor returns the on-disk path for a message's ciphertext blob.
func (s *Store) blobPathFor(bridgeID, senderLandID, msgID string) string {
	return filepath.Join(s.blobDir, bridgeID, senderLandID+"_"+msgID)
}

// writeBlob writes data atomically (temp file + rename) with 0600 perms,
// creating the bridge subdirectory at 0700.
func (s *Store) writeBlob(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create blob dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return fmt.Errorf("store: create blob temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("store: chmod blob: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("store: write blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("store: sync blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("store: close blob: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("store: rename blob: %w", err)
	}
	return nil
}

// readBlob loads a blob file.
func (s *Store) readBlob(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("store: read blob: %w", err)
	}
	return b, nil
}

// removeBlob deletes a blob file, ignoring "not found" so cleanup is idempotent.
func (s *Store) removeBlob(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
