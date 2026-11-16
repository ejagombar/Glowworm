# Rules for agents working on Glowworm

## Every tagged version must be compatible with the versions before it

Tags (`vX.Y`, `vX.Y.Z`) deploy straight to the live server, which holds real
financial data (see `deploy/README.md`). A new release has to pick up exactly
where any earlier tagged release left off, with no manual steps. That means:

- **Data.** A new build must open a data directory written by any earlier
  tagged version, and upgrade it by itself on start.
  - Change the schema only by adding a new migration in
    `internal/store/migrations/` with the next number.
  - Never edit, rename, renumber or delete a migration that has been in a
    tagged release. Some databases have already run it.
  - A migration must carry the existing data across, not just create the new
    shape: copy, convert or backfill rows. It must also work on any database
    an earlier release could have produced, including empty tables and
    defaults left unset.
  - Settings and other stored JSON (models, widget specs, …) keep reading
    their old keys and shapes, or a migration converts them.
- **Backups.** `glowworm restore` must accept backups made by any earlier
  version. If the backup format changes, bump its `version` and keep reading
  the old ones.
- **Configuration.** Environment variables (`GLOWWORM_*`), `glowworm.toml`
  keys, CLI commands and flags keep working. To rename one, read both names.
  Never quietly change what an existing setting means.
- **Links.** Page URLs keep working, so bookmarks and the home-screen app
  survive. Redirect a moved page instead of letting it 404.
- **Deployment contract.** These files are used as they are by the server and
  the release workflow, so keep them compatible:
  - the release asset name `glowworm-linux-arm64`
  - the `/opt/glowworm` layout
  - the `glowworm@` and `glowworm-backup@` units
  - `deploy.sh`'s arguments
  - `backup.sh`'s settings

  If one has to change, the release must also work with the server set up the
  old way, or the change needs steps a person runs first. Say so in the PR.
- **Rolling back.** Builds refuse a database whose schema is newer than they
  know about. Rolling back past a release that added a migration therefore
  means restoring the backup `deploy.sh` takes before each deploy. Prefer
  changes that don't need a migration. When one is needed, keep it additive
  (new tables and columns; leave old ones in place) and call it out in the PR
  description.

**Testing it.** Any change to the schema, to stored data or to the backup
format needs a test that starts from the old form and checks it comes through
the upgrade. For example: build a database at the previous migration, add rows
the old way, run the new migration, then read them back. The test must pass
before tagging.

## Working on the code

- Branch off `main` and open a PR. Don't push to `main` or tag releases unless
  the owner asks.
- Before committing, run `go vet ./...`, `go test ./...` and
  `golangci-lint run ./...`. All three must pass, and the release workflow
  runs them too.
