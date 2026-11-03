package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClassifyAttachmentLimits pins the P0 per-category byte limits (DEV_SPEC §9
// as revised: image 10 MiB, voice & video 25 MiB, other = envelope cap).
func TestClassifyAttachmentLimits(t *testing.T) {
	cases := []struct {
		mime     string
		category string
		limit    int64
	}{
		{"image/png", "image", 10 * 1024 * 1024},
		{"image/jpeg", "image", 10 * 1024 * 1024},
		{"audio/ogg", "voice", 25 * 1024 * 1024},
		{"audio/mpeg", "voice", 25 * 1024 * 1024},
		{"video/mp4", "video", 25 * 1024 * 1024},
		{"video/webm", "video", 25 * 1024 * 1024},
		{"application/pdf", "file", cliMaxEnvelopeBytes},
	}
	for _, tc := range cases {
		gotCat, gotLim := classifyAttachment(tc.mime)
		if gotCat != tc.category || gotLim != tc.limit {
			t.Errorf("classifyAttachment(%q) = (%q, %d), want (%q, %d)",
				tc.mime, gotCat, gotLim, tc.category, tc.limit)
		}
	}
}

// TestCheckAttachmentSizeBoundaries checks the voice/video/image size limits at
// the boundary without touching the filesystem (no large allocations).
func TestCheckAttachmentSizeBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		size   int64
		mime   string
		reject bool
	}{
		{"voice at limit", maxVoiceBytes, "audio/ogg", false},
		{"voice over limit", maxVoiceBytes + 1, "audio/ogg", true},
		{"video at limit", maxVideoBytes, "video/mp4", false},
		{"video over limit", maxVideoBytes + 1, "video/mp4", true},
		{"image at limit", maxImageBytes, "image/png", false},
		{"image over limit", maxImageBytes + 1, "image/png", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := checkAttachmentSize("x", tc.size, tc.mime)
			if tc.reject {
				if e == nil || e.Code != "attachment_too_large" {
					t.Fatalf("size %d (%s): got %v, want attachment_too_large", tc.size, tc.mime, e)
				}
			} else if e != nil {
				t.Fatalf("size %d (%s): unexpected error %v", tc.size, tc.mime, e)
			}
		})
	}
}

// sparseFile creates a file of exactly size bytes without writing them (a hole),
// so oversize cases cost no real disk or memory (DEV_SPEC §9 testing note).
func sparseFile(t *testing.T, name string, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate %s: %v", name, err)
	}
	return path
}

// TestLoadAttachmentsRejectsOversizeViaStat verifies an over-limit file is
// rejected on its stat size — before being read into memory. The files are
// sparse so the test stays cheap even at 25 MiB+.
func TestLoadAttachmentsRejectsOversizeViaStat(t *testing.T) {
	cases := []struct {
		name string
		size int64
	}{
		{"big.mp4", maxVideoBytes + 1}, // video over 25 MiB
		{"big.ogg", maxVoiceBytes + 1}, // voice over 25 MiB
		{"big.png", maxImageBytes + 1}, // image over 10 MiB
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sparseFile(t, tc.name, tc.size)
			_, e := loadAttachments([]string{path}, "")
			if e == nil || e.Code != "attachment_too_large" {
				t.Fatalf("loadAttachments(%s @ %d) = %v, want attachment_too_large", tc.name, tc.size, e)
			}
		})
	}
}

// TestLoadAttachmentsCombinedExceedsEnvelope verifies that several individually
// allowed attachments whose combined size overflows the envelope cap are
// rejected client-side with attachment_exceeds_envelope_limit, before any file
// is read (sparse files keep it cheap).
func TestLoadAttachmentsCombinedExceedsEnvelope(t *testing.T) {
	// Two 20 MiB videos: each < 25 MiB (allowed), together 40 MiB whose base64
	// (~53 MiB) blows past the 25 MiB envelope.
	a := sparseFile(t, "a.mp4", 20*1024*1024)
	b := sparseFile(t, "b.mp4", 20*1024*1024)
	_, e := loadAttachments([]string{a, b}, "")
	if e == nil || e.Code != "attachment_exceeds_envelope_limit" {
		t.Fatalf("combined oversize = %v, want attachment_exceeds_envelope_limit", e)
	}
}

// TestSendTooManyAttachments verifies the 5-per-message cap is a usage error.
func TestSendTooManyAttachments(t *testing.T) {
	dir := t.TempDir()
	var args []string
	for i := 0; i < maxAttachmentsPerMessage+1; i++ {
		p := filepath.Join(dir, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("hi"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		args = append(args, "--file", p)
	}
	args = append([]string{"send"}, append(args, "hello")...)
	_, stderr, exit := run(t, args...)
	if exit != exitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr=%s", exit, exitUsage, stderr)
	}
}
