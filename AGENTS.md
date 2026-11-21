# Local macOS installation

For local desktop installation and update tasks, including debug installs,
reuse the existing installation. Follow this workflow unless the user explicitly
requests a separate installation:

- Build with `make desktop`. Install the resulting desktop bundle at the fixed
  path `/Applications/AgentMux.app` and preserve its bundle identifier,
  `com.wails.agentmux-desktop`. Always launch that installed path.
- Do not create dated, versioned, `backup`, `previous`, or `pre-*` application
  copies in Applications, the repository, or Application Support. If a persistent
  rollback copy is needed, keep just one compressed archive at
  `~/Library/Application Support/agentmux/install-backups/previous.zip`.
- Prepare and validate the complete new bundle in a hidden temporary directory
  beside the target. Gracefully stop the app and its menu bar helper before
  replacement. Swap the entire bundle instead of merging files into the old app.
  Keep the old bundle inside that hidden temporary directory until the installed
  app starts successfully; restore it if replacement or startup fails. A
  successful `open` command alone does not establish application health.
- After successful installation, clean up the temporary directory and the
  verified, reproducible `.app` build output. If the build bundle must be retained,
  archive it instead of leaving another loose `.app` for macOS to index. Preserve
  source files, other build artifacts, application settings, and databases.
- Unregister the build and temporary bundle paths with `lsregister -u`, then
  refresh only `/Applications/AgentMux.app` with `lsregister -f`. Do not reset
  LaunchServices or Spotlight globally. Verify the installed app is running from
  the fixed path and that no extra AgentMux application entry was introduced.

# Checks before committing

- `make check BASE=main` runs only the checks your changes since the merge base
  with `main` need: committed, uncommitted and untracked files. It prints the
  plan, runs the selected checks in parallel and ends with a pass/fail summary.
  Go checks cover the changed packages, their importers and the packages whose
  tests import those, plus `internal/archguard` for any Go change.
  `make check-plan BASE=main` prints the plan without running it.
- `make check` (no `BASE`, or `ALL=1`) still runs every check; CI runs it on
  pushes to main and plans pull requests from the same table.
- The path-to-check table is `rules` in `scripts/checkplan/plan.go`, the only
  place that maps paths to checks. When a test starts reading a file outside
  its own package directory, add a rule naming that package; when a new tree
  needs a check, add it there, not to CI.
