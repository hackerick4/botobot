package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidMsgID(t *testing.T) {
	ok := []string{
		"c-20261029-0001",
		"a",
		"A.b_c-D",
		strings.Repeat("x", 64),
		"0123456789",
	}
	for _, s := range ok {
		if !ValidMsgID(s) {
			t.Errorf("ValidMsgID(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",
		strings.Repeat("x", 65),
		"has space",
		"slash/no",
		"colon:no",
		"unicodé",
		"new\nline",
	}
	for _, s := range bad {
		if ValidMsgID(s) {
			t.Errorf("ValidMsgID(%q) = true, want false", s)
		}
	}
}

// validHeader returns a structurally valid header for mutation in tests.
func validHeader() Header {
	return Header{
		V:                ProtocolVersion,
		BridgeID:         "br_testbridge0000000000",
		MsgID:            "c-20261029-0001",
		SenderLandID:     "land_sender000000000000",
		SenderKeyID:      "pk_sender00000000000000",
		LandSetVersion:   7,
		CreatedAt:        "2026-10-29T07:02:00.000Z",
		Mentions:         []string{"land_amy0000000000000000"},
		ReplyTo:          nil,
		Nonce:            bytes.Repeat([]byte{0x01}, NonceSize),
		CiphertextSHA256: bytes.Repeat([]byte{0x02}, SHA256Size),
		Recipients: []Recipient{
			{LandID: "land_amy0000000000000000", KeyID: "pk_amy0000000000000000", WrappedKeySHA256: bytes.Repeat([]byte{0x11}, SHA256Size)},
			{LandID: "land_bob0000000000000000", KeyID: "pk_bob0000000000000000", WrappedKeySHA256: bytes.Repeat([]byte{0x22}, SHA256Size)},
		},
	}
}

func TestHeaderValidate_OK(t *testing.T) {
	h := validHeader()
	if err := h.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestHeaderValidate_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Header)
	}{
		{"bad version", func(h *Header) { h.V = "bb-test-v1" }},
		{"empty bridge", func(h *Header) { h.BridgeID = "" }},
		{"bad msg_id", func(h *Header) { h.MsgID = "has space" }},
		{"empty sender", func(h *Header) { h.SenderLandID = "" }},
		{"empty sender key", func(h *Header) { h.SenderKeyID = "" }},
		{"zero land_set_version", func(h *Header) { h.LandSetVersion = 0 }},
		{"bad created_at", func(h *Header) { h.CreatedAt = "yesterday" }},
		{"empty mention", func(h *Header) { h.Mentions = []string{""} }},
		{"short nonce", func(h *Header) { h.Nonce = h.Nonce[:8] }},
		{"short ct hash", func(h *Header) { h.CiphertextSHA256 = h.CiphertextSHA256[:8] }},
		{"no recipients", func(h *Header) { h.Recipients = nil }},
		{"recipient empty land", func(h *Header) { h.Recipients[0].LandID = "" }},
		{"recipient empty key", func(h *Header) { h.Recipients[0].KeyID = "" }},
		{"recipient short hash", func(h *Header) { h.Recipients[0].WrappedKeySHA256 = []byte{1, 2, 3} }},
		{"recipients unsorted", func(h *Header) { h.Recipients[0], h.Recipients[1] = h.Recipients[1], h.Recipients[0] }},
		{"duplicate recipient", func(h *Header) { h.Recipients[1].LandID = h.Recipients[0].LandID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := validHeader()
			tc.mutate(&h)
			if err := h.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}
}

func TestEnvelopeValidate(t *testing.T) {
	h := validHeader()
	env := &Envelope{
		Header:     h,
		Signature:  bytes.Repeat([]byte{0x03}, SignatureSize),
		Ciphertext: bytes.Repeat([]byte{0x04}, 48),
		WrappedKeys: map[string][]byte{
			"land_amy0000000000000000": bytes.Repeat([]byte{0x05}, WrappedKeySize),
			"land_bob0000000000000000": bytes.Repeat([]byte{0x06}, WrappedKeySize),
		},
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"short signature", func(e *Envelope) { e.Signature = e.Signature[:10] }},
		{"empty ciphertext", func(e *Envelope) { e.Ciphertext = nil }},
		{"extra wrapped key", func(e *Envelope) {
			e.WrappedKeys["land_zzz0000000000000000"] = bytes.Repeat([]byte{0x07}, WrappedKeySize)
		}},
		{"missing wrapped key", func(e *Envelope) { delete(e.WrappedKeys, "land_amy0000000000000000") }},
		{"short wrapped key", func(e *Envelope) { e.WrappedKeys["land_amy0000000000000000"] = []byte{1, 2, 3} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// rebuild a fresh valid envelope each time
			e := &Envelope{
				Header:     validHeader(),
				Signature:  bytes.Repeat([]byte{0x03}, SignatureSize),
				Ciphertext: bytes.Repeat([]byte{0x04}, 48),
				WrappedKeys: map[string][]byte{
					"land_amy0000000000000000": bytes.Repeat([]byte{0x05}, WrappedKeySize),
					"land_bob0000000000000000": bytes.Repeat([]byte{0x06}, WrappedKeySize),
				},
			}
			tc.mutate(e)
			if err := e.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}
}
