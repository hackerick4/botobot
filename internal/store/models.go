package store

// Row models mirror the DEV_SPEC §5 schema. Times are Unix milliseconds (UTC);
// nullable columns use pointers. These are deliberately close to the tables so
// the query layer stays thin; the HTTP layer (M3/M4) maps them to API objects.

// Bridge mirrors the bridges table.
type Bridge struct {
	ID                  string
	Name                string
	Description         string
	Status              string // active | closed
	AdminLandID         *string
	InviteCodeEnc       []byte
	InviteCodeRotatedAt *int64
	// InviteCodeUsed marks a one-time code already consumed by a join, and
	// InviteCodeExpiresAt (Unix ms) is when the code stops working; a rotate
	// resets both (Batch 1 item 5).
	InviteCodeUsed      bool
	InviteCodeExpiresAt *int64
	MaxLands            int
	LandSetVersion      int64
	LandsCanInvite      bool
	JoinLocked          bool
	JoinLockedReason    *string
	RateLimitPerMin     int
	QueueQuotaBytes     int64
	QueueBytes          int64
	AdminLastActiveAt   int64
	// AdminJoinsAckedThrough is the joined_at (Unix ms) of the most recent land
	// whose land_joined notice the admin has acked; joins at or before it are
	// never rebuilt (DEV_SPEC §11 re-delivery).
	AdminJoinsAckedThrough int64
	CreatedAt              int64
	ClosedAt               *int64
	CloseReason            *string

	// Pending is true while a /join-created bridge still awaits its first claim
	// (no admin land yet); ClaimTokenHash is the HMAC of the one-time admin claim
	// token (nil once claimed) and ClaimExpiresAt is when an unclaimed pending
	// bridge is deleted (DEV_SPEC §6 /join, §19).
	Pending        bool
	ClaimTokenHash []byte
	ClaimExpiresAt *int64

	// Pair marks a two-bot bridge (v0.5.0, migration 0007): at most two lands,
	// no rotate once the peer joined, and a member leaving or being removed
	// closes the bridge. False for bridges created before v0.5.0.
	Pair bool

	// EntryID is set on a bridge created through a public entry (v0.6.0,
	// migration 0008); nil for every other bridge.
	EntryID *string
	// EntryClosedAt is the close time of the bridge's public entry when that
	// entry is closed (v0.6.1; read-only, derived): a line that closed at or
	// after it ended because the whole entry closed.
	EntryClosedAt *int64

	// GraceUntil (Unix ms) and GraceCloser (land id) are set while a closed
	// bridge still lets the other side fetch its last queued deliveries
	// (v0.6.4, migration 0009); both nil otherwise.
	GraceUntil  *int64
	GraceCloser *string
}

// Land mirrors the lands table.
type Land struct {
	ID              string
	BridgeID        string
	Role            string // admin | member
	Handle          string
	DisplayName     string
	OwnerName       string
	ActiveKeyID     *string
	PollIntervalSec int
	LastPolledAt    *int64
	JoinedAt        int64
	LeftAt          *int64
	LeftReason      *string
}

// PublicKey mirrors the public_keys table.
type PublicKey struct {
	ID           string
	LandID       string
	EncPub       []byte // 32
	SignPub      []byte // 32
	BindingSig   []byte // 64
	Fingerprint  string
	CreatedAt    int64
	SupersededAt *int64
}

// APIKey mirrors the api_keys table (key_hash is never exposed outside store).
type APIKey struct {
	ID         string
	LandID     string
	Prefix     string
	KeyHash    []byte
	CreatedAt  int64
	LastUsedAt *int64
	RevokedAt  *int64
}

// Delivery is one row of a message's per-recipient delivery status.
type Delivery struct {
	RecipientLandID string
	Status          string // queued | delivered | expired | dropped
	UpdatedAt       int64
}

// InboxItem is one leased inbox row returned by FetchInbox. For kind=message the
// message fields are populated (ciphertext resolved from the DB or a blob file);
// for kind=event only EventJSON is set.
type InboxItem struct {
	ID         string
	Kind       string // message | event
	EnqueuedAt int64
	ExpiresAt  int64
	FetchCount int

	// kind=message
	SenderLandID string
	MsgID        string
	HeaderJSON   []byte
	Signature    []byte // 64-byte Ed25519 envelope signature (§7.5)
	Ciphertext   []byte
	WrappedKey   []byte // 80

	// kind=event
	EventJSON []byte
}
