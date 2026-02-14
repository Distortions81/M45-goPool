# AGENTS.md

## Changelog Workflow

- Write changelogs under `documentation/changes/`.
- Use one file per change set, named `YYYY-MM-DD-<branch>-vs-<base>.md`.
- Start from `documentation/changes/CHANGELOG_TEMPLATE.md`.

## Style Rules

- Keep entries outcome-focused and concrete.
- Prefer exact values/endpoints over adjectives.
- Keep bullets short and single-purpose.
- Avoid filler words (`improved`, `optimized`, `refined`) unless paired with a specific result.

## Standard Sections

- `## <Branch> vs <Base>`
- `- **Scope:** <N files>, +<insertions>/-<deletions>`
- `### Runtime / VarDiff`
- `### Saved Workers / Storage / Privacy`
- `### Saved Workers Runtime`
- `### Overview / API / Caching`
- `### Admin / Ops`
- `### Docs / Tests`

## Current Example

- Latest entry: `documentation/changes/2026-02-14-experimental-vs-main.md`
