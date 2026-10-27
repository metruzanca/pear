# Pear

Declarative macOS settings for people who keep their configuration in a
dotfiles repo.

Pear reads a TOML file, compares it with the current state of the Mac, and
writes only what differs. Every setting comes from a curated catalog, so there
is no arbitrary `defaults` or shell escape hatch: a typo fails loudly, and every
value is read back and verified after it is written.

```toml
# ~/.config/pear/config.toml
config_version = 1

[dock]
autohide = true
autohide_delay = 0.25

[finder]
show_hidden = true
```

```console
$ pear plan
Plan: 2 change(s), 1 unchanged
  SET  dock.autohide     false -> true  restart Dock
  SET  finder.show_hidden false -> true restart Finder

Run `pear apply` to apply.

$ pear apply
Plan: 2 change(s), 1 unchanged
  SET  dock.autohide      false -> true  restart Dock
  SET  finder.show_hidden false -> true  restart Finder
Applying...
Applied 2 change(s), 1 unchanged
Restarted Dock.
Restarted Finder.
Snapshot: /Users/you/.local/state/pear/backups/20261027T143012Z
```

Running `pear apply` again makes no changes.

## Install

Development requires Go (managed with [mise](https://mise.jdx.dev) in this
repo):

```console
$ mise install
$ make build
$ ./pear version
```

Release binaries and a Homebrew tap are not available yet.

## Commands

| Command | Purpose |
| --- | --- |
| `pear apply` | Apply the config. Idempotent; prints the plan first. |
| `pear plan` | Show changes without writing. Exits 1 when changes exist. |
| `pear validate` | Parse and validate the config. Works on Linux. |
| `pear configure` | Interactively edit the config in a TUI. `pear configur` is an alias. |
| `pear list` | List the catalog. `--json` for machines, `--scope user` to filter. |
| `pear explain <id>` | Show what a setting maps to, including its current value. |
| `pear doctor` | Check the config, catalog, state directory, and tools. |
| `pear snapshots` | List snapshots created by previous applies. |
| `pear restore [name]` | Restore a snapshot (latest by default). |

Useful flags:

- `--config PATH` or `PEAR_CONFIG` to point at another file.
- `--only dock,finder` to limit an apply, plan, or configure session to one or
  more catalog groups.
- `--dry-run` on `apply`.
- `--json` on `apply`, `plan`, and `list`.

## Interactive editor

`pear configure` opens a TUI for the config. It works anywhere Go runs (the
macOS current-value column only appears on macOS).

- `↑`/`↓` move, `PgUp`/`PgDn` page, `Home`/`End` jump.
- `Enter` edits, `Esc` cancels.
- `/` searches, `Tab` cycles All / Configured / Unset / Pending.
- `u` removes a value from the config; `x` adds a reset (with confirmation).
- `ctrl+s` saves, `ctrl+r` refreshes macOS values, `q` quits.

Values use typed editors: selects for bools and enums, validated inputs for
numbers, strings, and paths, and a multi-line editor for lists. Arrow keys only:
there are no `h`/`j`/`k`/`l`/`g`/`G` bindings.

Saving patches only the edited keys, so comments and unrelated formatting
survive. Each save is backed up under
`~/.local/state/pear/config-backups/<timestamp>/`, validated, then written
atomically. After a successful save the TUI offers to apply the changes with
the same snapshot and verification path as `pear apply`.

## Config discovery

Pear looks in this order:

1. `--config PATH`
2. `$PEAR_CONFIG`
3. `$XDG_CONFIG_HOME/pear/config.toml`
4. `~/.config/pear/config.toml`

An optional `include = ["modules/dock.toml"]` list loads files relative to the
including file. Includes apply in order, then the including file. A value in a
later file cancels an earlier reset, and a reset in a later file cancels an
earlier value. Setting and resetting the same key within one file is an error.
`reset = ["dock.tilesize"]` deletes a setting on apply; absent keys are simply
unmanaged.

Every root file must declare `config_version = 1`; included files may omit it.

## Safety model

- Pear never writes settings that are not in the config. It never prunes.
- A snapshot of every changed setting is written before any write happens.
  If the snapshot cannot be created, `apply` refuses to run. `restore` also
  snapshots the current values first, so a mistaken restore can be undone.
- Values are read back after writing. A write that does not stick is a failure.
- Unknown keys, wrong types, and invalid enum values are errors with a
  suggestion, not silent no-ops.
- Restarts happen only for settings that actually changed, after all writes.
  Pear never runs `killall cfprefsd`.
- The catalog is the API. Adding a setting is a data change plus a test that
  proves read/write/read-back on a real Mac.
- The interactive editor never writes the config until `ctrl+s`, keeps a
  timestamped backup of the previous file, and refuses to replace it if the
  edited result does not validate.

## Current scope

This first milestone covers user-scoped, `defaults`-backed settings: Dock,
Finder, keyboard and input, window/text behavior, screenshots, and the menu bar
clock.

Not implemented yet:

- Host-scoped (`ByHost`) and system-scoped settings, including `sudo` handling
  and the partial-apply workflow.
- Command-backed settings such as `pmset`, `scutil`, and `systemsetup`.
- Dock application lists, Finder sidebar items, and keyboard shortcuts.
- Backup/restore for non-plist settings.

File management stays out of scope on purpose. Use stow, chezmoi, or yadm for
your dotfiles; Pear only handles macOS preferences.

## Development

```console
$ mise install
$ make check
```

Catalog entries live in `internal/catalog/catalog.toml`. The engine is driven by
the catalog: `internal/engine` plans, writes, verifies, and snapshots, while
`internal/adapter/defaults` is the only place that invokes `defaults(1)`.

Tests use a fake runner, so they do not touch the machine. A real integration
test is intentionally not part of `go test` yet; verify a new catalog entry on a
disposable macOS user or VM before adding it.
