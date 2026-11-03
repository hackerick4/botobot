package cli

// Server response shapes the CLI decodes (DEV_SPEC §6.3, §6.4). Only the fields
// the CLI uses are declared; unknown fields are ignored on decode.

// bridgeResp is the server's Bridge output (DEV_SPEC §6.3).
type bridgeResp struct {
	BridgeID       string `json:"bridge_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	AdminLandID    string `json:"admin_land_id"`
	LandCount      int    `json:"land_count"`
	MaxLands       int    `json:"max_lands"`
	Pair           bool   `json:"pair"`
	LandSetVersion int64  `json:"land_set_version"`
	InviteLink     string `json:"invite_link"`
}

// createJoinResp is the body of POST /bridges and POST .../join (DEV_SPEC §6.4).
// invite_code is present only for create.
type createJoinResp struct {
	Bridge     bridgeResp `json:"bridge"`
	Land       cachedLand `json:"land"`
	APIKey     string     `json:"api_key"`
	InviteCode string     `json:"invite_code"`
}

// landsResp is GET /lands (DEV_SPEC §6.4).
type landsResp struct {
	LandSetVersion int64        `json:"land_set_version"`
	Lands          []cachedLand `json:"lands"`
}

// landResp wraps the single-land response of PATCH /me and PUT /me/public-key.
type landResp struct {
	Land cachedLand `json:"land"`
}

// inviteCodeResp is GET /invite-code and the rotate response (DEV_SPEC §6.4).
type inviteCodeResp struct {
	InviteCode string `json:"invite_code"`
	RotatedAt  string `json:"rotated_at"`
	InviteLink string `json:"invite_link"`
	// Usable is nil from servers before v0.4.8 (treated as usable).
	Usable *bool `json:"usable"`
}

// delivery is one per-recipient delivery entry in send/status responses.
type delivery struct {
	LandID    string `json:"land_id"`
	Handle    string `json:"handle,omitempty"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// sendResp is POST /send (DEV_SPEC §6.4).
type sendResp struct {
	MsgID      string     `json:"msg_id"`
	ExpiresAt  string     `json:"expires_at"`
	Deliveries []delivery `json:"deliveries"`
}

// statusResp is GET /messages/{id}/status (DEV_SPEC §6.4).
type statusResp struct {
	MsgID      string     `json:"msg_id"`
	Deliveries []delivery `json:"deliveries"`
}

// apiKeyResp is POST /me/api-keys (DEV_SPEC §6.4).
type apiKeyResp struct {
	KeyID  string `json:"key_id"`
	APIKey string `json:"api_key"`
}

// removedResp is DELETE /lands/{id} (DEV_SPEC §6.4).
type removedResp struct {
	Removed      string `json:"removed"`
	BridgeClosed bool   `json:"bridge_closed"`
	closeOutcome
}

// closeOutcome (v0.6.4) is what a close reports about this bot's messages
// the other side had not read: how many, and until when it can still get them.
type closeOutcome struct {
	Undelivered int    `json:"undelivered"`
	FinalUntil  string `json:"final_until"`
}

// pingResp is POST /me/ping (DEV_SPEC §6.4).
type pingResp struct {
	OK         bool   `json:"ok"`
	ServerTime string `json:"server_time"`
}

// ---- request bodies ----

type keyInputBody struct {
	EncPub     []byte `json:"enc_pub"`
	SignPub    []byte `json:"sign_pub"`
	BindingSig []byte `json:"binding_sig"`
}

type profileBody struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	OwnerName   string `json:"owner_name"`
}

type landBlockBody struct {
	Profile profileBody  `json:"profile"`
	Key     keyInputBody `json:"key"`
}
