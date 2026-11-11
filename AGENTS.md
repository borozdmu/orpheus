# AGENTS.md

## Task branches and shared CI

1. Before starting or resuming a task, check the repository (`git remote -v`), local changes (`git status -sb`), and worktrees (`git worktree list`). Run `git fetch --prune origin` against the team's fork. If the fetch fails, restore access to the current `origin/main` before proceeding.
2. Look for an open PR and a local or remote branch for this task. Continue in its existing worktree and compare it with its remote branch and the fetched `origin/main`. If main has advanced, review and integrate its changes while preserving collaborators' commits and uncommitted work.
3. For a new independent task, create one branch from the fetched `origin/main` (for example, `git switch -c codex/<task> origin/main` in a clean checkout). When working with a local `main`, update it using `git merge --ff-only origin/main`. Read-only work needs no branch.
4. Fetch main again before merging. Review the diff and required checks for the final PR revision. After merge, clean up only this task's branch and clean worktree once the changes are included and no one is using them.

After merging to main, shared CI runs automatically. Before releasing a verified set, match the intended product commit to its entry in the [verified manifest](https://github.com/borozdmu/ai-agents-releases-ci/blob/latest-verified/latest.json).

Read [README.md](README.md).

# Environment
* Go 1.27

# General rules
* Do not preserve backward compatibility.
* Choose the simplest implementation that fully meets the current requirements.
* Prefer established, well-maintained libraries over custom implementations.
* Fix the cause, not the symptom.
* Suggest best practices, even if they may require refactoring.

# Development workflow
* Before releasing, wait for this project's exact SHA in `latest-verified/latest.json`; see [README.md: Release compatibility](README.md#release-compatibility).
* Always write a comprehensive test suite covering the implementation alongside the implementation itself.
* API-first workflow: spec, `make generate`, implementation, tests.
* Run `make fix gofix check` after completing the implementation.

# Database and migrations
* Use sqlc for database queries and Goose for migrations.
* Edit SQL in `internal/store/queries/`, then run `make generate`; do not edit `internal/store/db/` by hand.
* Keep data changes and DDL operations in separate migrations.
* Test new migrations in both directions against the test database.

# Docs
* Write documentation in English.
* Follow the principles of Maxim Ilyakhov's "Write, Cut": be concise without losing substance.
* Prefer clear structure, diagrams, and lists over long blocks of text.
