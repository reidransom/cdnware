# AGENTS.md

This file provides guidance to WARP (warp.dev) when working with code in this repository.

## Overview

`cdnware` is a Go CLI tool that revisions static assets (adds content hash to filenames) and updates references in HTML/CSS/JS files for CDN deployment. Designed for Jekyll sites but works with any static site generator.

## Build & Run

```bash
just build                                  # Build binary
just test                                   # Run tests
./cdnware -cdn https://cdn.example.com _site  # Run against a site directory
./cdnware -cdn https://cdn.example.com -src static -dest static-rev _site  # Custom directories
```

## Release

Releases are automated via GoReleaser. Push a version tag to trigger:
```bash
git tag v1.0.0 && git push --tags
```

## Code Architecture

Single-file application (`cdnware.go`) with this flow:

1. **Asset inventory** (`newRevisioner()`):
   - Walks every regular file under `<baseDir>/assets/`
   - Keys assets by their root-relative `/assets/...` URL

2. **Content-graph revisioning** (`revisioner.revise()` → `writeAsset()`):
   - Rewrites absolute and relative asset-to-asset references before hashing
   - Rejects reference cycles because cyclic content-addressed filenames are not stable
   - Creates an 8-character MD5 content hash
   - Flattens source directories into `<baseDir>/assets-rev/`; manifest keys retain source paths
   - Removes stale destination output before each run
   - Shares identical bytes at a flat revisioned filename; rejects conflicting bytes rather than overwriting

3. **Reference replacement** (`useman()` → `repFile()`):
   - Walks `<baseDir>` for textual site files, excluding `assets/` and `assets-rev/`
   - Replaces every exact `/assets/...` URL, including URLs inside `srcset`

4. **Output**: JSON manifest mapping original paths to CDN or same-origin revisioned URLs

### Configuration

`cdn`, `src`, and `dest` can be set via CLI flags or a `cdnware.{toml,yaml,yml,json}` config file auto-discovered in SITEROOT or CWD. Precedence: flag > file > default. Use `-config <path>` to point at a specific file, or `-config -` to disable auto-discovery.

### Asset handling

- Every regular file under the source directory is revisioned; extensions do not gate deployment.
- Asset references are rewritten inside `.css`, `.html`, `.js`, `.json`, `.mjs`, `.svg`, `.toml`, `.webmanifest`, and `.xml` assets.
- Site references are rewritten in the same textual file types.
- The `-cdn` flag optionally prepends a base URL; an empty value produces same-origin `/assets-rev/...` URLs.
- The `-src` and `-dest` flags override the default `assets` and `assets-rev` directories.

## Agent skills

### Issue tracker

Issues live as Markdown files under `.scratch/<feature>/`. See `.scratch/agents/issue-tracker.md`.

### Triage labels

The canonical five triage roles use their default label strings. See `.scratch/agents/triage-labels.md`.

### Domain docs

This repository uses a single-context layout. See `.scratch/agents/domain.md`.
