# AGENTS.md

## Commits

Use Angular-style Conventional Commits for commit messages and pull request titles.

Before committing or opening/updating a pull request, verify that the final commit or squash title follows `type(scope): description` or `type: description`, using an Angular Conventional Commit type such as `build`, `chore`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `revert`, `style`, or `test`.

Do not assume that a syntactically valid type will appear in release notes. When changing semantic-release release rules, verify that any type that can trigger a release is also configured to appear in generated release notes.
