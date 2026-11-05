# Update recovery

Before replacing an installation, the updater stores its previous executable (or
complete macOS app bundle) and a SQLite `VACUUM INTO` snapshot under
`<state home>/rollback`. Only a completely written pair becomes current. SHA-256
checks detect damaged or incomplete checkpoint files before any restore changes.
The next successful preparation prunes earlier generations.

Stop the daemon, then run `tmx rollback` (`tmx-go rollback` when the executable is
named tmx-go). The command takes the daemon's single-instance lock and restores
both members. It does not start a daemon. Start tmx again after it succeeds.
Database changes made after the checkpoint are reverted by rollback.

Daemon startup preserves the pre-update checkpoint while applying migrations.
A migration error restores both members automatically and exits with a message
to start again. Clean boots proactively retain the proven executable and a
consistent database snapshot, refreshing only when the installation bytes change.
Direct/manual upgrades use that earlier pair instead of copying the already
replaced executable before migration. Without an earlier clean boot, startup
still snapshots the database before migration, marking the binary as unproven.
Recovery of that checkpoint restores only the database, leaves the live binary
untouched, and asks the operator to install a compatible binary before restarting.
Fresh databases establish their first checkpoint
after successful initialization.

Startup with pending migrations or an updater marker preserves the old pair;
the next clean boot can refresh it after a successful update. Repeated boots with
unchanged bytes retain the same database snapshot, so rollback also reverts writes
since that snapshot. Each pair is capped at 1 GiB; publication keeps one pair,
with space for a second while preparing it. An oversized preparation fails without
replacing the previous pair. SQLite page counts include committed WAL contents in
the size preflight; the completed snapshot is checked again before publication.
Failure to refresh on a clean boot does not block startup: the daemon logs a
warning and exposes `rollback checkpoint stale: <reason>` in `tmx status` and the
authenticated snapshot response. Failure to prepare the first pre-migration
snapshot does block migration, leaving the database unchanged.

A restore uses an on-disk marker because two filesystem replacements cannot be
one atomic rename. Startup completes a marked restore before opening SQLite and
then exits. If a filesystem operation fails, repeat rollback after resolving the
error; the checkpoint and displaced recovery files remain. All SQLite users must
be closed during restoration. WAL, SHM, and rollback-journal sidecars are moved
aside with the displaced database, never replayed onto the restored snapshot.

The updater snapshot is a consistent point in time before replacement; writes
between that snapshot and daemon shutdown are intentionally outside the retained
checkpoint. Backups are local recovery data, not an authentication or signing
boundary against an actor who can rewrite the installation and its state home.

Tests use temporary SQLite databases and text files standing in for binaries;
they never execute installers or launch daemons.
