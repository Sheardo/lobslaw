package memory

// Bucket names inside state.db. Each record type lives in its own
// top-level bbolt bucket, keyed by record ID.
const (
	BucketPolicyRules     = "policy_rules"
	BucketScheduledTasks  = "scheduled_tasks"
	BucketCommitments     = "commitments"
	BucketAuditEntries    = "audit_entries"
	BucketVectorRecords   = "vector_records"
	BucketEpisodicRecords = "episodic_records"
	BucketStorageMounts   = "storage_mounts"
	// BucketChannelState holds per-channel resume state for gateway
	// channels (telegram update offset, REST cursors, webhook
	// last-seen timestamps). Keyed by "<channel>:<key>" — single
	// bucket avoids per-channel bucket proliferation while keeping
	// scans cheap and predictable.
	BucketChannelState = "channel_state"
	// BucketSoulTune holds the cluster-wide agent personality overlay
	// — name, emotive dimensions, fragments. Single record keyed by
	// SoulTuneRecordID. Replaces the local SOUL.md write path so
	// container deployments don't need a writable file mount.
	BucketSoulTune = "soul_tune"
	// BucketCredentials holds OAuth (and other) credentials the
	// operator has connected to the cluster. Tokens are encrypted
	// at rest with the cluster MemoryKey; the bucket bytes are
	// ciphertext. Keyed by "<provider>:<subject>" — one record per
	// (provider, authenticated-user) tuple.
	BucketCredentials = "credentials"
	// BucketSessions holds the per-conversation index record: which
	// channel + user, the retained sequence range, the title. Keyed
	// by "<channel>:<channel_id>". One record per live conversation.
	BucketSessions = "sessions"
	// BucketSessionMessages holds the transcript bodies, keyed
	// "<session_id>:<20-digit zero-padded seq>". The padding makes
	// bbolt's byte ordering identical to sequence ordering, so a
	// session's thread is an ordered prefix scan and trimming is a
	// delete of the lowest keys.
	BucketSessionMessages = "session_messages"
	// BucketUserPrefs holds per-user preferences: timezone,
	// subscribed channel addresses (telegram chat_id, future Slack
	// user, etc.), language. Keyed by canonical user_id. Plaintext
	// — channel IDs aren't secret, timezones aren't secret.
	// Solo-deployment uses one record (id=owner); team/corporate
	// deployments scale by adding records.
	BucketUserPrefs = "user_prefs"

	// BucketSessionLeases holds cluster-wide turn ownership, one
	// record per conversation. Separate from BucketSessions because a
	// lease is written three times per turn (claim, heartbeats,
	// release) and the transcript is written once — sharing a record
	// would make every lease write contend with the append made by
	// the turn holding it.
	BucketSessionLeases = "session_leases"

	// BucketPrompts holds pending confirmations. Raft-backed rather
	// than per-process so an approval tapped on one node resolves a
	// prompt issued by another, and so a restart does not lose the
	// turn the user was answering. See R2.
	BucketPrompts = "prompts"

	// BucketConsolidations holds Dream's adjudication log: what it
	// decided about each cluster of near-duplicate memories and why.
	// Read by `lobslaw memory consolidations`; pruned by Dream itself
	// so a long-lived cluster does not accumulate a record per cluster
	// per night forever.
	BucketConsolidations = "consolidations"

	// BucketPinned holds the always-on memory blocks rendered into
	// every system prompt: the user profile and the agent's notes.
	// Small and capped by design — it is a fixed tax on every request.
	BucketPinned = "pinned"
)

// SoulTuneRecordID is the constant key under BucketSoulTune. There
// is one tune record per cluster — the agent has one identity.
const SoulTuneRecordID = "soul:tune"

// allBuckets lists every bucket the store ensures exists on open.
var allBuckets = []string{
	BucketPolicyRules,
	BucketScheduledTasks,
	BucketCommitments,
	BucketAuditEntries,
	BucketVectorRecords,
	BucketEpisodicRecords,
	BucketStorageMounts,
	BucketChannelState,
	BucketSoulTune,
	BucketCredentials,
	BucketUserPrefs,
	BucketSessions,
	BucketSessionMessages,
	BucketSessionLeases,
	BucketPrompts,
	BucketConsolidations,
	BucketPinned,
}
