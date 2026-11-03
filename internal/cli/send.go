package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hackerick4/botobot/internal/judge"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hackerick4/botobot/internal/protocol"
)

// Attachment limits enforced by the CLI before encryption (DEV_SPEC §9). The
// spec states these in "MB"; we use binary MiB (the server's envelope cap is
// 25 MiB), which is the stricter, simpler interpretation (DEV_SPEC §19). P0
// caps are size-only: voice and video are both 25 MiB (down from the former
// 100 MiB video cap), the per-message count is 5, and the duration ceilings
// (10 min voice / 5 min video) are dropped because size is the binding limit
// and there is no stdlib media probe — duration is always left unset. Chunked
// upload (secretstream) for larger media is deferred to P1 (M10).
const (
	maxAttachmentsPerMessage = 5
	maxImageBytes            = 10 * 1024 * 1024 // image/*
	maxVoiceBytes            = 25 * 1024 * 1024 // audio/*
	maxVideoBytes            = 25 * 1024 * 1024 // video/*
	aeadTagSize              = 16               // XChaCha20-Poly1305 tag (DEV_SPEC §7.1)
	// cliMaxEnvelopeBytes mirrors the server default MAX_ENVELOPE_BYTES (25 MiB,
	// DEV_SPEC §9). The encrypted envelope (header + ciphertext + wrapped keys)
	// must fit; a larger attachment yields attachment_exceeds_envelope_limit.
	cliMaxEnvelopeBytes = 26214400
	maxOtherBytes       = cliMaxEnvelopeBytes // non-media files
)

// maxStdinBodyBytes caps how much a --stdin message body may be before it is
// rejected (Batch 1 item 2). It mirrors the envelope cap; the precise
// per-message envelope-size check still runs after the text is known.
const maxStdinBodyBytes = cliMaxEnvelopeBytes

func init() { commands["send"] = cmdSend }

// readStdinBody reads a message body from the app's stdin, enforcing a size cap
// and rejecting an empty body (Batch 1 item 2). Documented flows pass the body
// via a quoted heredoc (<<'EOF') so the shell never expands it and the text
// never appears in argv. Content is preserved verbatim apart from trimming
// trailing newlines (the heredoc/here-string artifact); a body containing
// $(...) or backticks is passed through unchanged and is never executed.
func readStdinBody(a *app) (string, *cliError) {
	if a.stdin == nil {
		return "", usageErrorf("--stdin given but no input is attached")
	}
	b, err := io.ReadAll(io.LimitReader(a.stdin, maxStdinBodyBytes+1))
	if err != nil {
		return "", generalErrorf("read stdin: %v", err)
	}
	if int64(len(b)) > maxStdinBodyBytes {
		return "", &cliError{Code: "message_too_large", Message: "stdin message body is too large", Exit: exitGeneral}
	}
	text := strings.TrimRight(string(b), "\n")
	if text == "" {
		return "", usageErrorf("stdin message body is empty")
	}
	return text, nil
}

// stringSlice collects a repeatable string flag (e.g. --mention a --mention b).
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

// cmdSend implements `send` (DEV_SPEC §12.3): refresh the land set, encrypt to
// every other land, and post the envelope. A recipients_mismatch triggers one
// automatic refresh-and-retry.
func cmdSend(a *app, args []string) *cliError {
	var to string
	var mentions stringSlice
	var files stringSlice
	var replyTo, msgID string
	var stdin bool
	g, fs, e := a.parse("send", args, func(fs *flag.FlagSet) {
		fs.StringVar(&to, "to", "", "comma-separated handles to mention")
		fs.Var(&mentions, "mention", "handle to mention (repeatable)")
		fs.Var(&files, "file", "attachment file path (repeatable)")
		fs.StringVar(&replyTo, "reply-to", "", "msg_id this message replies to")
		fs.StringVar(&msgID, "msg-id", "", "client message id (default: generated)")
		fs.BoolVar(&stdin, "stdin", false, "read the message body from stdin (preferred; use a quoted heredoc)")
	})
	if e != nil {
		return e
	}
	// Message body: from stdin (preferred, keeps text out of argv) or the
	// positional args (kept for compatibility; not used by documented flows).
	var text string
	if stdin {
		if len(fs.Args()) > 0 {
			return usageErrorf("send: pass the message via --stdin OR as TEXT, not both")
		}
		var ce *cliError
		if text, ce = readStdinBody(a); ce != nil {
			return ce
		}
	} else {
		text = strings.Join(fs.Args(), " ")
	}
	if text == "" && len(files) == 0 {
		return usageErrorf("send: provide a message (via --stdin or TEXT) and/or at least one --file")
	}
	if len(files) > maxAttachmentsPerMessage {
		return usageErrorf("send: at most %d attachments per message", maxAttachmentsPerMessage)
	}
	if msgID != "" && !protocol.ValidMsgID(msgID) {
		return usageErrorf("send: --msg-id must match [A-Za-z0-9._-]{1,64}")
	}
	if replyTo != "" && !protocol.ValidMsgID(replyTo) {
		return usageErrorf("send: --reply-to must be a msg_id ([A-Za-z0-9._-]{1,64})")
	}

	// Build the requested mention handle set from --to and --mention.
	mentionHandles := map[string]struct{}{}
	for _, h := range strings.Split(to, ",") {
		if h = strings.TrimSpace(h); h != "" {
			mentionHandles[h] = struct{}{}
		}
	}
	for _, h := range mentions {
		if h = strings.TrimSpace(h); h != "" {
			mentionHandles[h] = struct{}{}
		}
	}

	return runSend(a, g, sendRequest{
		text:           text,
		files:          files,
		mentionHandles: mentionHandles,
		replyTo:        replyTo,
		msgID:          msgID,
	})
}

// sendRequest is one message to deliver, shared by `send` and `tick reply`.
type sendRequest struct {
	text           string
	files          []string
	mentionHandles map[string]struct{}
	replyTo        string
	msgID          string
	// preloaded are attachments already read (used by `release`, which must
	// send exactly the held bytes, not re-read files).
	preloaded []protocol.Attachment
}

// runSend performs the full send: attachment checks, session/bridge resolution,
// land refresh, the TOFU trust gate, the chat-loop send limits, and the encrypt
// + POST with a single recipients_mismatch retry. It prints the "sent" record.
// It is the shared core behind `send` and `tick reply` (DEV_SPEC §12.3).
func runSend(a *app, g *globalFlags, req sendRequest) *cliError {
	if len(req.files) > maxAttachmentsPerMessage {
		return usageErrorf("send: at most %d attachments per message", maxAttachmentsPerMessage)
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	// Load and check attachments before any network call (DEV_SPEC §9), with
	// the P0 credential-path denylist and private-key content sniff.
	attachments := req.preloaded
	if attachments == nil {
		attachments, e = loadAttachments(req.files, sess.ks.home)
		if e != nil {
			return e
		}
	}
	// P0 hard block (EXFIL_DEFENSE.md S-BB): a message carrying a bot-bridge
	// key or this home's private keys is never sent, whoever approves it.
	secrets := localSecrets(sess.ks)
	if containsSecret([]byte(req.text), secrets) {
		return errSecretBlocked()
	}
	for _, at := range attachments {
		if containsSecret(at.Data, secrets) {
			return errSecretBlocked()
		}
	}
	// Credential formats (API keys, tokens, private keys, passwords) are a
	// hard block as well: the owner's "send" cannot release them, because
	// release reruns this same path.
	if judge.Outbound(req.text).HasCredential() {
		return errSecretBlocked()
	}
	for _, at := range attachments {
		if isTextual(at.Mime, at.Data) {
			data := at.Data
			if len(data) > maxTextScanBytes {
				data = data[:maxTextScanBytes]
			}
			if judge.Outbound(string(data)).HasCredential() {
				return errSecretBlocked()
			}
		}
	}
	bs, e := sess.resolveBridgeStrict()
	if e != nil {
		return e
	}
	km, err := sess.ks.loadKeys(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read keys: %v", err)
	}

	msgID := req.msgID
	if msgID == "" {
		msgID = a.newMsgID()
	}

	// Risk-based hold (EXFIL_DEFENSE.md §3.0): attachments, DLP medium or
	// above, or a tainted bridge wait for the owner's confirmation card.
	if !a.releasing {
		reasons, e := sess.assessSend(bs.bc.BridgeID, req.text, attachments)
		if e != nil {
			return e
		}
		if len(reasons) > 0 {
			return sess.createHold(&heldItem{
				Kind:     holdKindMsg,
				Bridge:   bs.bc.BridgeID,
				Text:     req.text,
				Atts:     attachments,
				ReplyTo:  req.replyTo,
				Mentions: sortedKeys(req.mentionHandles),
				MsgID:    msgID,
				Reasons:  reasons,
			})
		}
	}

	// Chat-loop limits (Batch 1 item 7): refuse before doing any work if the
	// per-session or hourly auto-reply cap is reached, surfacing a NOTIFY_OWNER
	// line so the routine tells the owner and pauses.
	if e := sess.checkSendLimit(bs.bc.BridgeID); e != nil {
		if e.Code == "send_limit" {
			sess.pr.raw(sendLimitNotify)
			if a.releasing {
				_ = sess.pr.flush()
				return e // keep the held item; it was not sent
			}
			return sess.pr.done()
		}
		return e
	}

	ctx := context.Background()
	lc, e := bs.refreshLands(ctx, sess.ks)
	if e != nil {
		return e
	}

	params := sendParams{
		msgID:          msgID,
		text:           req.text,
		attachments:    attachments,
		mentionHandles: req.mentionHandles,
		replyTo:        req.replyTo,
	}
	resp, e := sess.trySend(ctx, bs, km, lc, params)
	if e != nil {
		// A stale land set is retried once after a forced refresh (DEV_SPEC §12.3).
		if e.Code == "recipients_mismatch" {
			lc, e2 := bs.refreshLands(ctx, sess.ks)
			if e2 != nil {
				return e2
			}
			resp, e = sess.trySend(ctx, bs, km, lc, params)
		}
		if e != nil {
			return e
		}
	}

	// Commit the send against the chat-loop counters (best-effort: the message is
	// already delivered, so a persistence error must not fail the command).
	_ = sess.recordSend(bs.bc.BridgeID)

	sess.pr.emit(map[string]any{
		"type":       "sent",
		"msg_id":     resp.MsgID,
		"deliveries": resp.Deliveries,
	})
	return sess.pr.done()
}

type sendParams struct {
	msgID          string
	text           string
	attachments    []protocol.Attachment
	mentionHandles map[string]struct{}
	replyTo        string
}

// trySend builds, encrypts, and posts one envelope against the given land set.
func (s *session) trySend(ctx context.Context, bs *bridgeState, km *keyMaterial, lc *landsCache, p sendParams) (*sendResp, *cliError) {
	// Recipients = every other land, with its active enc key (DEV_SPEC §6.4).
	var recips []protocol.RecipientKey
	handleToID := map[string]string{}
	for i := range lc.Lands {
		l := &lc.Lands[i]
		handleToID[l.Handle] = l.LandID
		if l.LandID == bs.bc.LandID {
			continue
		}
		if len(l.EncPub) != protocol.KeySize || l.KeyID == "" {
			continue // land without an active key yet; server would reject anyway
		}
		recips = append(recips, protocol.RecipientKey{LandID: l.LandID, KeyID: l.KeyID, EncPub: l.EncPub})
	}
	if len(recips) == 0 {
		return nil, generalErrorf("send: the other bot has not joined this bridge yet")
	}

	// TOFU: every recipient's current key must be pinned and trusted. The server
	// requires the recipient set to be exactly all other lands, so a single
	// untrusted (new/changed/invalid) member pauses all sending until the owner
	// runs `trust` (Batch 1 item 4).
	if ce := s.checkRecipientsTrusted(bs, lc); ce != nil {
		return nil, ce
	}

	// Resolve mention handles to land ids (DEV_SPEC §6.4 header.mentions).
	var mentionIDs []string
	for h := range p.mentionHandles {
		id, ok := handleToID[h]
		if !ok {
			return nil, usageErrorf("send: unknown mention handle %q", h)
		}
		mentionIDs = append(mentionIDs, id)
	}

	// Plaintext payload (DEV_SPEC §7.3), encrypted end-to-end.
	now := s.app.now().UTC()
	payload := protocol.Payload{
		Type:        "message",
		Text:        p.text,
		Attachments: p.attachments,
		SentAt:      now.Format(rfc3339Milli),
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, generalErrorf("encode payload: %v", err)
	}
	// Pre-encryption envelope-size guard (DEV_SPEC §9 video note). The ciphertext
	// is the plaintext plus the 16-byte tag; add a margin for the header and the
	// 80-byte wrapped key per recipient.
	margin := int64(1024 + protocol.WrappedKeySize*len(recips))
	if int64(len(plaintext))+aeadTagSize+margin > cliMaxEnvelopeBytes {
		return nil, errEnvelopeTooLarge()
	}

	var replyPtr *string
	if p.replyTo != "" {
		replyPtr = &p.replyTo
	}
	env, err := protocol.Encrypt(protocol.EncryptInput{
		BridgeID:       bs.bc.BridgeID,
		MsgID:          p.msgID,
		SenderLandID:   bs.bc.LandID,
		SenderKeyID:    km.KeyID,
		SenderSignPriv: km.SignPriv,
		LandSetVersion: lc.LandSetVersion,
		CreatedAt:      now.Format(rfc3339Milli),
		Mentions:       mentionIDs,
		ReplyTo:        replyPtr,
		Plaintext:      plaintext,
		Recipients:     recips,
	})
	if err != nil {
		return nil, generalErrorf("encrypt: %v", err)
	}

	var resp sendResp
	if ce := bs.cli.do(ctx, http.MethodPost, "/v1/bridges/"+bs.bc.BridgeID+"/send", env, &resp); ce != nil {
		return nil, ce
	}
	return &resp, nil
}

// checkRecipientsTrusted reconciles the pin store against the current land set
// and refuses the send if any other land's current key is not pinned+trusted
// (Batch 1 item 4). It persists any newly-observed pending entries so a later
// tick can notify the owner.
func (s *session) checkRecipientsTrusted(bs *bridgeState, lc *landsCache) *cliError {
	pins, err := s.ks.loadPins(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read pins: %v", err)
	}
	if _, changed := pins.reconcile(lc, bs.bc.LandID, s.app.now().UnixMilli()); changed {
		if err := s.ks.savePins(bs.bc.BridgeID, pins); err != nil {
			return generalErrorf("save pins: %v", err)
		}
	}
	var untrusted []string
	for i := range lc.Lands {
		l := &lc.Lands[i]
		if l.LandID == bs.bc.LandID || l.KeyID == "" {
			continue
		}
		if !pins.isTrusted(l) {
			untrusted = append(untrusted, l.Handle)
		}
	}
	if len(untrusted) > 0 {
		why := "the key of %s changed since it was first seen (a reinstall, or someone posing as them)"
		if pins.ExpectAdminFP != "" && pins.Locked == "" {
			return &cliError{Code: "checking_admin", Exit: exitGeneral, Message: "sending waits: the admin key from the invite has not been checked yet. Nothing to ask your owner; the next tick checks it automatically, then try again"}
		}
		if pins.Locked != "" {
			why = "this bridge is locked because the admin fingerprint did not match the invite (%s not trusted)"
		}
		return &cliError{
			Code: "untrusted_members",
			Message: fmt.Sprintf("sending paused: "+why+". Tell your owner that in one sentence and ask once whether to keep trusting them; only if they say yes run: bot-bridge-cli trust --bridge %s <member> and release it. Do not ask the owner to compare fingerprints",
				strings.Join(untrusted, ", "), bs.bc.BridgeID),
			Exit: exitGeneral,
		}
	}
	return nil
}

// loadAttachments validates each file against the per-category size limit and
// the combined envelope estimate *before* reading its contents, then reads the
// surviving files and builds the protocol.Attachment list (DEV_SPEC §9, §7.3).
// Rejecting on the file's stat size means an oversize attachment is never read
// into memory.
func loadAttachments(paths []string, bbHome string) ([]protocol.Attachment, *cliError) {
	if len(paths) == 0 {
		return nil, nil
	}
	for _, path := range paths {
		if e := checkAttachmentPath(path, bbHome); e != nil {
			return nil, e
		}
	}
	// Phase 1 (stat only): detect each type and enforce its size limit.
	mimes := make([]string, len(paths))
	var total int64
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, generalErrorf("read attachment %q: %v", path, err)
		}
		if info.IsDir() {
			return nil, generalErrorf("attachment %q is a directory", path)
		}
		mimeType, err := detectMimeFile(path)
		if err != nil {
			return nil, generalErrorf("read attachment %q: %v", path, err)
		}
		if e := checkAttachmentSize(filepath.Base(path), info.Size(), mimeType); e != nil {
			return nil, e
		}
		mimes[i] = mimeType
		total += info.Size()
	}
	// Combined guard: the base64 of the attachment bytes is the irreducible part
	// of the plaintext, so if it alone overflows the envelope the message cannot
	// fit regardless of the text. Reject here before reading any file; the exact
	// check in buildEnvelope still applies once the text is known (DEV_SPEC §9).
	if base64Len(total)+aeadTagSize > cliMaxEnvelopeBytes {
		return nil, errEnvelopeTooLarge()
	}

	// Phase 2: read the surviving files and build the attachment list.
	out := make([]protocol.Attachment, 0, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, generalErrorf("read attachment %q: %v", path, err)
		}
		if sniffSecretContent(data) {
			return nil, errAttachmentDenied("contains private key or API key material")
		}
		sum := sha256.Sum256(data)
		out = append(out, protocol.Attachment{
			Name:        sanitizeFilename(filepath.Base(path)),
			Mime:        mimes[i],
			Size:        int64(len(data)),
			SHA256:      sum[:],
			Data:        data,
			DurationSec: nil, // P0: size-only caps, no duration probe (DEV_SPEC §9, §19)
		})
	}
	return out, nil
}

// checkAttachmentSize enforces one file's per-category byte limit (DEV_SPEC §9).
// It is pure (no I/O) so the boundaries are unit-testable without large files.
func checkAttachmentSize(name string, size int64, mimeType string) *cliError {
	category, limit := classifyAttachment(mimeType)
	if size > limit {
		return &cliError{
			Code:    "attachment_too_large",
			Message: fmt.Sprintf("%s attachment exceeds the %d-byte limit for %s", name, limit, category),
			Exit:    exitGeneral,
		}
	}
	return nil
}

// errEnvelopeTooLarge is the client-side rejection when a message cannot fit the
// envelope cap (DEV_SPEC §9). Chunked upload for larger media is P1 (M10, §19).
func errEnvelopeTooLarge() *cliError {
	return &cliError{
		Code:    "attachment_exceeds_envelope_limit",
		Message: fmt.Sprintf("message is too large to send; the envelope limit is %d bytes", cliMaxEnvelopeBytes),
		Exit:    exitGeneral,
	}
}

// base64Len returns the length of the standard base64 encoding of n bytes.
func base64Len(n int64) int64 { return (n + 2) / 3 * 4 }

// detectMimeFile detects an attachment's MIME type from the first 512 bytes
// (what http.DetectContentType inspects) without reading the whole file.
func detectMimeFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var head [512]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return detectMime(path, head[:n]), nil
}

// detectMime determines an attachment's MIME type from its content
// (http.DetectContentType) falling back to its extension (DEV_SPEC §9).
func detectMime(path string, data []byte) string {
	byContent := http.DetectContentType(data)
	if byContent != "application/octet-stream" {
		// DetectContentType appends "; charset=..."; keep just the media type.
		if i := strings.IndexByte(byContent, ';'); i >= 0 {
			return strings.TrimSpace(byContent[:i])
		}
		return byContent
	}
	if ext := filepath.Ext(path); ext != "" {
		if byExt := mime.TypeByExtension(ext); byExt != "" {
			if i := strings.IndexByte(byExt, ';'); i >= 0 {
				return strings.TrimSpace(byExt[:i])
			}
			return byExt
		}
	}
	return "application/octet-stream"
}

// classifyAttachment maps a MIME type to its category and byte limit (DEV_SPEC §9).
func classifyAttachment(mimeType string) (category string, limit int64) {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image", maxImageBytes
	case strings.HasPrefix(mimeType, "audio/"):
		return "voice", maxVoiceBytes
	case strings.HasPrefix(mimeType, "video/"):
		return "video", maxVideoBytes
	default:
		return "file", maxOtherBytes
	}
}
