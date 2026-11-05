package store

import "fmt"

// migrations are applied in order, each exactly once, inside a transaction. Never edit an
// applied migration; append a new one.
var migrations = []string{
	// 1: initial schema.
	`
CREATE TABLE agents (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	project_id TEXT,
	parent_id TEXT,
	level TEXT NOT NULL,
	harness TEXT NOT NULL,
	mode TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	cwd TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	brief TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	desired TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	pid INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	last_activity_at INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0,
	turns INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX agents_project ON agents(project_id);

CREATE TABLE projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	folder TEXT NOT NULL,
	brief TEXT NOT NULL DEFAULT '',
	lead_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE messages (
	id TEXT PRIMARY KEY,
	from_id TEXT NOT NULL,
	to_id TEXT NOT NULL,
	body TEXT NOT NULL,
	state TEXT NOT NULL,
	reply_to TEXT,
	created_at INTEGER NOT NULL,
	delivered_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_to_state ON messages(to_id, state);
CREATE INDEX messages_from_to ON messages(from_id, to_id, created_at);

CREATE TABLE approvals (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	tool TEXT NOT NULL DEFAULT '',
	input TEXT,
	description TEXT NOT NULL DEFAULT '',
	question TEXT NOT NULL DEFAULT '',
	options TEXT NOT NULL DEFAULT '[]',
	state TEXT NOT NULL,
	answer TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX approvals_state ON approvals(state);

CREATE TABLE timeline (
	agent_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	at INTEGER NOT NULL,
	kind TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT '',
	tool TEXT NOT NULL DEFAULT '',
	tool_use_id TEXT NOT NULL DEFAULT '',
	input TEXT,
	is_error INTEGER NOT NULL DEFAULT 0,
	meta TEXT,
	PRIMARY KEY (agent_id, seq)
) WITHOUT ROWID;
`,
	// 2: agent profiles. Existing agents get the built-in profile of their level.
	`
ALTER TABLE agents ADD COLUMN profile TEXT NOT NULL DEFAULT '';
UPDATE agents SET profile = level WHERE profile = '';

CREATE TABLE profiles (
	slug TEXT PRIMARY KEY,
	data TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
`,
	// 3: chats between tmx installs (DESIGN.md "Chats"): contacts, chats, members, messages,
	// delivery receipts and the outbox of envelopes waiting for an offline peer.
	`
CREATE TABLE contacts (
	peer_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	remote_name TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	safety_code TEXT NOT NULL DEFAULT '',
	added_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE chats (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	peer_id TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	agent_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 0,
	read_seq INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	last_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE chat_members (
	chat_id TEXT NOT NULL,
	peer_id TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL,
	joined_at INTEGER NOT NULL,
	left_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (chat_id, peer_id)
) WITHOUT ROWID;

CREATE TABLE chat_messages (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id TEXT NOT NULL,
	id TEXT NOT NULL,
	sender TEXT NOT NULL,
	sender_name TEXT NOT NULL DEFAULT '',
	sender_agent TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL,
	at INTEGER NOT NULL,
	state TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	recipients INTEGER NOT NULL DEFAULT 0,
	UNIQUE (chat_id, id)
);
CREATE INDEX chat_messages_chat ON chat_messages(chat_id, seq);

CREATE TABLE chat_receipts (
	chat_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	peer_id TEXT NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (chat_id, message_id, peer_id)
) WITHOUT ROWID;

CREATE TABLE chat_outbox (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	peer_id TEXT NOT NULL,
	chat_id TEXT NOT NULL DEFAULT '',
	message_id TEXT NOT NULL DEFAULT '',
	envelope TEXT NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	next_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX chat_outbox_peer ON chat_outbox(peer_id, seq);
`,
	// 4: tasks handed to Companion (DESIGN.md "Companion"); the task is JSON, state and times
	// are columns for listing.
	`
CREATE TABLE companion_tasks (
	id TEXT PRIMARY KEY,
	state TEXT NOT NULL,
	data TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX companion_tasks_state ON companion_tasks(state);
`,
	// 5: chat files and @mentions (DESIGN.md "Chats"): files attached to messages (downloaded
	// separately), mentions per message, and each member install's chat agent (for @mentions).
	`
ALTER TABLE chat_messages ADD COLUMN mentions TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_messages ADD COLUMN mentions_me INTEGER NOT NULL DEFAULT 0;

CREATE TABLE chat_files (
	chat_id TEXT NOT NULL,
	id TEXT NOT NULL,
	message_id TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL,
	name TEXT NOT NULL,
	type TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	state TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	tries INTEGER NOT NULL DEFAULT 0,
	next_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;
CREATE INDEX chat_files_message ON chat_files(chat_id, message_id);
CREATE INDEX chat_files_state ON chat_files(state, next_at);

CREATE TABLE chat_agents (
	chat_id TEXT NOT NULL,
	peer_id TEXT NOT NULL,
	agent TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (chat_id, peer_id)
) WITHOUT ROWID;
`,
	// 6: the board (DESIGN.md "Board"): threads, and the thread of head timeline entries and
	// of mail and questions to and from the head. Every existing project gets its thread.
	`
CREATE TABLE threads (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	kind TEXT NOT NULL,
	project_id TEXT NOT NULL DEFAULT '',
	chat_id TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '',
	archived INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
INSERT INTO threads (id, title, kind, created_at, updated_at) VALUES ('general', 'General', 'general', 0, 0);
INSERT INTO threads (id, title, kind, project_id, created_at, updated_at)
	SELECT 'p-' || id, name, 'project', id, created_at, updated_at FROM projects;
ALTER TABLE timeline ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
`,
	// 7: the board's logs and order (DESIGN.md "Board"): append-only entries per thread, and
	// the owner's urgency order of the threads.
	`
ALTER TABLE threads ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
CREATE TABLE thread_entries (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	text TEXT NOT NULL,
	link TEXT NOT NULL DEFAULT '',
	card INTEGER NOT NULL DEFAULT 0,
	by_name TEXT NOT NULL,
	by_id TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	corrects TEXT NOT NULL DEFAULT ''
);
CREATE INDEX thread_entries_thread ON thread_entries(thread_id, at);
`,
	// 8: handing a group's admin role over (DESIGN.md "Chats", Groups): the signed certificate
	// chain that proves who the admin is (JSON, at most 8), our open offer to a member (JSON),
	// and the capabilities other installs advertise (e.g. "admin1": they understand handovers).
	`
ALTER TABLE chats ADD COLUMN admin_proof TEXT NOT NULL DEFAULT '';
ALTER TABLE chats ADD COLUMN admin_offer TEXT NOT NULL DEFAULT '';
CREATE TABLE peer_caps (
	peer_id TEXT PRIMARY KEY,
	caps TEXT NOT NULL,
	updated_at INTEGER NOT NULL
) WITHOUT ROWID;
`,
	// 9: chat boards (DESIGN.md "Chat boards"): each chat's topics (every field a
	// last-writer-wins register: value, time, peer), which topic each message hangs on (a
	// register too; a move may arrive before its message), the topics' logs, and this owner's
	// own order of a board's strings.
	`
CREATE TABLE chat_topics (
	chat_id TEXT NOT NULL,
	id TEXT NOT NULL,
	title TEXT NOT NULL,
	title_at INTEGER NOT NULL DEFAULT 0,
	title_by TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '',
	color_at INTEGER NOT NULL DEFAULT 0,
	color_by TEXT NOT NULL DEFAULT '',
	archived INTEGER NOT NULL DEFAULT 0,
	archived_at INTEGER NOT NULL DEFAULT 0,
	archived_by TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE TABLE chat_message_topics (
	chat_id TEXT NOT NULL,
	message_id TEXT NOT NULL,
	topic_id TEXT NOT NULL,
	at INTEGER NOT NULL,
	by_peer TEXT NOT NULL,
	PRIMARY KEY (chat_id, message_id)
) WITHOUT ROWID;

CREATE TABLE chat_topic_entries (
	chat_id TEXT NOT NULL,
	id TEXT NOT NULL,
	topic_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	text TEXT NOT NULL,
	link TEXT NOT NULL DEFAULT '',
	by_peer TEXT NOT NULL,
	by_name TEXT NOT NULL DEFAULT '',
	by_agent TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	corrects TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;
CREATE INDEX chat_topic_entries_at ON chat_topic_entries(chat_id, at);

CREATE TABLE chat_boards (
	chat_id TEXT PRIMARY KEY,
	topic_order TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
`,
	// 10: voice transcripts (DESIGN.md "Voice"): one per source, searchable through an FTS5 index on
	// the text. n is an explicit row number, so the index survives a VACUUM; audio is the WAV a
	// job left by a restart starts again from (never in the API).
	`
CREATE TABLE transcripts (
	n INTEGER PRIMARY KEY,
	id TEXT NOT NULL UNIQUE,
	source TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	language TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	segments TEXT NOT NULL DEFAULT '[]',
	error TEXT NOT NULL DEFAULT '',
	audio TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX transcripts_created ON transcripts(created_at);
CREATE VIRTUAL TABLE transcripts_fts USING fts5(text, content='transcripts', content_rowid='n', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER transcripts_ai AFTER INSERT ON transcripts BEGIN
	INSERT INTO transcripts_fts(rowid, text) VALUES (new.n, new.text);
END;
CREATE TRIGGER transcripts_ad AFTER DELETE ON transcripts BEGIN
	INSERT INTO transcripts_fts(transcripts_fts, rowid, text) VALUES ('delete', old.n, old.text);
END;
CREATE TRIGGER transcripts_au AFTER UPDATE OF text ON transcripts BEGIN
	INSERT INTO transcripts_fts(transcripts_fts, rowid, text) VALUES ('delete', old.n, old.text);
	INSERT INTO transcripts_fts(rowid, text) VALUES (new.n, new.text);
END;
`,
	// 11: recordings of the owner's screen and microphone (DESIGN.md "Recorder"); the files
	// live in recordings/<id>/, the rows name them.
	`
CREATE TABLE recordings (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	kind TEXT NOT NULL,
	state TEXT NOT NULL,
	mime TEXT NOT NULL,
	mic INTEGER NOT NULL DEFAULT 0,
	bytes INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	resumed_at INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL,
	ended_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX recordings_started ON recordings(started_at);
CREATE TABLE recording_segments (
	recording_id TEXT NOT NULL,
	n INTEGER NOT NULL,
	file TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0,
	wav TEXT NOT NULL DEFAULT '',
	wav_bytes INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL,
	PRIMARY KEY (recording_id, n)
) WITHOUT ROWID;
`,
	// 12: the device's own sound in screen recordings (DESIGN.md "Recorder"); frames need no
	// table (they are files in recordings/<id>/frames/).
	`
ALTER TABLE recordings ADD COLUMN system_audio INTEGER NOT NULL DEFAULT 0;
`,
	// 13: native recordings (DESIGN.md "Recorder"): who writes a recording's files (web: the
	// window's chunks; native: the macOS app's ScreenCaptureKit).
	`
ALTER TABLE recordings ADD COLUMN source TEXT NOT NULL DEFAULT 'web';
`,
	// 14: summaries of recordings, written by Claude (DESIGN.md "Summaries"): one per recording; the
	// lists are JSON arrays.
	`
CREATE TABLE recording_summaries (
	recording_id TEXT PRIMARY KEY,
	state TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	key_points TEXT NOT NULL DEFAULT '[]',
	decisions TEXT NOT NULL DEFAULT '[]',
	action_items TEXT NOT NULL DEFAULT '[]',
	chapters TEXT NOT NULL DEFAULT '[]',
	transcripts INTEGER NOT NULL DEFAULT 0,
	frames INTEGER NOT NULL DEFAULT 0,
	parts INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	requested INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX recording_summaries_state ON recording_summaries(state, updated_at);
`,
	// 15: WhatsApp (DESIGN.md "WhatsApp"): the linked account's chats, messages, reactions and
	// contacts, filled from the history sync and live events; wa_meta: the linked account and
	// when it was linked. media_ref says how to fetch an
	// attachment from WhatsApp (its keys; never in the API), media_file is the file once
	// downloaded on demand. origin: who sent it through tmx ("owner" or an agent's id). The
	// device's own keys are not here: they live in secrets/whatsapp.db (whatsmeow's store).
	`
CREATE TABLE wa_chats (
	jid TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	unread INTEGER NOT NULL DEFAULT 0,
	pinned INTEGER NOT NULL DEFAULT 0,
	muted INTEGER NOT NULL DEFAULT 0,
	archived INTEGER NOT NULL DEFAULT 0,
	avatar TEXT NOT NULL DEFAULT '',
	last_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX wa_chats_last ON wa_chats(last_at);
CREATE TABLE wa_messages (
	chat_jid TEXT NOT NULL,
	id TEXT NOT NULL,
	sender_jid TEXT NOT NULL DEFAULT '',
	sender_name TEXT NOT NULL DEFAULT '',
	from_me INTEGER NOT NULL DEFAULT 0,
	at INTEGER NOT NULL,
	type TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT '',
	caption TEXT NOT NULL DEFAULT '',
	media TEXT NOT NULL DEFAULT '',
	media_ref TEXT NOT NULL DEFAULT '',
	media_file TEXT NOT NULL DEFAULT '',
	quoted TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	edited INTEGER NOT NULL DEFAULT 0,
	deleted INTEGER NOT NULL DEFAULT 0,
	origin TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (chat_jid, id)
) WITHOUT ROWID;
CREATE INDEX wa_messages_at ON wa_messages(chat_jid, at);
CREATE INDEX wa_messages_origin ON wa_messages(origin, at) WHERE origin != '';
CREATE TABLE wa_reactions (
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	by_jid TEXT NOT NULL,
	by_name TEXT NOT NULL DEFAULT '',
	emoji TEXT NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, message_id, by_jid)
) WITHOUT ROWID;
CREATE TABLE wa_contacts (
	jid TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	push_name TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
CREATE TABLE wa_meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
	// Project reports and durable, idempotent owner feedback.
	`
CREATE TABLE project_sections (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 data TEXT NOT NULL
);
CREATE TABLE project_feedback (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 data TEXT NOT NULL,
 PRIMARY KEY(project_id, id)
);
`,
	// Local mail retry identities and durable delivery-effect receipts.
	`
ALTER TABLE messages ADD COLUMN retry_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN retry_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN receipt TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN receipt_at INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX messages_retry ON messages(from_id, retry_id) WHERE retry_id != '';
`,
	`ALTER TABLE approvals ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE approvals ADD COLUMN card TEXT NOT NULL DEFAULT 'null';`,
	`ALTER TABLE approvals ADD COLUMN deferred INTEGER NOT NULL DEFAULT 0;
ALTER TABLE approvals ADD COLUMN answer_id TEXT NOT NULL DEFAULT '';
CREATE TABLE card_answers (approval_id TEXT NOT NULL, request_id TEXT NOT NULL, request TEXT NOT NULL, result TEXT NOT NULL, message_id TEXT NOT NULL, PRIMARY KEY (approval_id, request_id));`,
	// Checklist progress, optimistic versions and a conservative ETA (DESIGN.md "Progress"):
	// the latest checklist lives on the project row itself; progress_samples is bounded
	// history (100 per project, pruned after 30 days) used only to derive an ETA.
	`
ALTER TABLE projects ADD COLUMN progress INTEGER;
ALTER TABLE projects ADD COLUMN progress_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN progress_state TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN progress_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN progress_updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN progress_run TEXT NOT NULL DEFAULT '';
CREATE TABLE progress_samples (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	at INTEGER NOT NULL,
	run TEXT NOT NULL,
	percent INTEGER NOT NULL,
	state TEXT NOT NULL,
	PRIMARY KEY (project_id, version)
);
CREATE INDEX progress_samples_at ON progress_samples(at);
`,
	`CREATE TABLE project_contributions (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	agent_id TEXT NOT NULL,
	data TEXT NOT NULL,
	PRIMARY KEY(project_id, agent_id)
);`,
	// Peer mail (DESIGN.md "Python fleet interop"): each local message to or from an agent on
	// another machine, with its wire ID on that machine; outbound rows carry the send retry
	// (next_attempt_at > 0: still to be sent).
	`CREATE TABLE peer_messages (
	local_id TEXT PRIMARY KEY,
	peer TEXT NOT NULL,
	wire_id TEXT NOT NULL,
	direction TEXT NOT NULL,
	sha256 TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	UNIQUE(peer, wire_id)
);
CREATE INDEX peer_messages_due ON peer_messages(next_attempt_at) WHERE next_attempt_at > 0;`,
	// The Matrix (DESIGN.md "Matrix"): a thread's owner, status and lifecycle, the owner's words and the story
	// it serves, its parent and its place on the canvas; and its links to everything working on it
	// (never deleted: detaching sets ended_at; at most one open link per thread, axis, ref and role).
	`
ALTER TABLE threads ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN status TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN lifecycle TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN owner TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN story TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN intent_text TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN intent_source TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN canvas_x REAL NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN canvas_y REAL NOT NULL DEFAULT 0;
CREATE TABLE thread_links (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL,
	axis TEXT NOT NULL,
	ref TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	why TEXT NOT NULL DEFAULT '',
	by_name TEXT NOT NULL,
	how TEXT NOT NULL,
	at INTEGER NOT NULL,
	ended_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX thread_links_open ON thread_links(thread_id, axis, ref, role) WHERE ended_at = 0;
CREATE INDEX thread_links_ref ON thread_links(axis, ref);
`,
	`ALTER TABLE agents ADD COLUMN shelved_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN done_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN protected INTEGER NOT NULL DEFAULT 0;`,
	// recordings' sensitivity (G5, the owner 3 Nov): "clinic" = patient data of the clinics, summarized on our own
	// machines only; "" = anything may write the report.
	`ALTER TABLE recordings ADD COLUMN sensitivity TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE profile_default_upgrades (name TEXT PRIMARY KEY);`,
	`CREATE TABLE project_imports (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, source TEXT NOT NULL, id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(project_id,source,id));`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	cur, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("database schema %d is newer than this tmx (%d); upgrade tmx", cur, len(migrations))
	}
	for v := cur + 1; v <= len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v-1]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
