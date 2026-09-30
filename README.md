# cdnware

Rev your static assets and update all references to them to prep your site
for deploy to a CDN (or anywhere else) with version mapping.

## Install

<!-- Pre-built binaries are distributed in [releases](https://github.com/reidransom/cdnware/releases). -->

You can build from source,

```
git clone https://github.com/reidransom/cdnware
cd cdnware
go build 
sudo mv cdnware /usr/local/bin # optional
```

## Usage

Suppose you built your jekyll site to the standard `_site` folder.

```
$ cdnware -cdn https://cdn.example.com/some-path _site
```

Every file in `_site/assets` is copied to `_site/assets-rev` with an 8-character content hash. Nested directories are preserved. References in the generated site and between textual assets are rewritten before hashing, including responsive-image `srcset` URLs and relative JavaScript imports. The source assets remain unchanged, and a JSON manifest is printed to standard output.

Use `-rev-ext .css,.js` to revise only those extensions (case-insensitive).
Excluded files stay in `assets` and do not appear in `assets-rev` or the
manifest. Site references to them remain unchanged; relative references from
revisioned assets to excluded files become same-origin `/assets/...` URLs so
they still resolve after the referring asset moves.

Ex:

```
$ tree _site
_site
├── assets
│   ├── image.png
│   ├── script.js
│   └── styles.css
└── index.html

2 directories, 4 files
$ cat _site/index.html 
<html>
  <head>
    <link rel="stylesheet" href="/assets/styles.css">
  </head>
  <img src="/assets/image.png">
  <script src="/assets/script.js"></script>
</html>
$ cdnware -cdn https://cdn.example.com/some-path _site
{
  "/assets/image.png": "https://cdn.example.com/some-path/assets-rev/image.e15d28a4.png",
  "/assets/script.js": "https://cdn.example.com/some-path/assets-rev/script.dada41ac.js",
  "/assets/styles.css": "https://cdn.example.com/some-path/assets-rev/styles.1f81c53a.css"
}
$ tree _site
_site
├── assets
│   ├── image.png
│   ├── script.js
│   └── styles.css
├── assets-rev
│   ├── image.e15d28a4.png
│   ├── script.dada41ac.js
│   └── styles.1f81c53a.css
└── index.html

3 directories, 7 files
$ cat _site/index.html 
<html>
  <head>
    <link rel="stylesheet" href="https://cdn.example.com/some-path/assets-rev/styles.1f81c53a.css">
  </head>
  <img src="https://cdn.example.com/some-path/assets-rev/image.e15d28a4.png">
  <script src="https://cdn.example.com/some-path/assets-rev/script.dada41ac.js"></script>
</html>
```

## Configuration

All settings can be set via CLI flag or a config file. Precedence is
**flag > config file > embedded base config**.

### Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `-cdn` | `""` | CDN base URL |
| `-src` | `assets` | Source asset directory (relative to SITEROOT) |
| `-dest` | `assets-rev` | Destination directory for revisioned assets |
| `-rev-ext` | _all files_ | Comma-separated extensions with leading dots, e.g. `.css,.js`; `-rev-ext ""` revises none |
| `-config` | _auto_ | Path to config file; use `-` to disable auto-discovery |

### Config file

If `-config` is not given, cdnware looks in SITEROOT then CWD for, in order:
`cdnware.toml`, `cdnware.yaml`, `cdnware.yml`, `cdnware.json`. First match wins.

[`cdnware.example.toml`](cdnware.example.toml) is embedded in the binary at
build time and loaded first. Settings omitted from a site config inherit its
values, even when `-config -` disables auto-discovery. Edit the example and
rebuild to change the shipped defaults; use a site config or flags for local
overrides. The embedded file leaves `rev_ext` unset because the default is to
revise every file; `rev_ext = []` would instead revise nothing.

```toml
# cdnware.toml
cdn = "https://cdn.example.com/v3"
src = "assets"
dest = "assets-rev"
rev_ext = [".css", ".js"]
```

```yaml
# cdnware.yml
cdn: https://cdn.example.com/v3
src: assets
dest: assets-rev
rev_ext: [.css, .js]
```

```json
{
  "cdn": "https://cdn.example.com/v3",
  "src": "assets",
  "dest": "assets-rev",
  "rev_ext": [".css", ".js"]
}
```

With the shipped base config, omitting `rev_ext` revises every regular source
file, including files without an extension. An explicit empty list
(`rev_ext = []` in TOML, `rev_ext: []` in YAML, or `"rev_ext": []` in JSON)
revises nothing: the manifest is `{}` and no destination assets are written.
The CLI equivalent is `-rev-ext ""`.
Flag selection overrides the file list, including when the flag is empty.
Extensions must start with `.` and contain only letters and digits; duplicates
(ignoring case) and malformed entries are errors.

## Philosophy

The command keeps one interface for the full deployment transformation: point it at the generated site. It inventories the asset tree, resolves the asset-reference graph, writes content-addressed files, and rewrites generated references. Cyclic asset references fail the build because mutually content-addressed filenames cannot be stable.
