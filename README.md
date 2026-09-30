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
$ cdnware --cdn https://cdn.example.com/some-path _site
```

Every file in `_site/assets` is copied to `_site/assets-rev` with an 8-character content hash. Nested directories are preserved. References in the generated site and between textual assets are rewritten before hashing, including responsive-image `srcset` URLs and relative JavaScript imports. The source assets remain unchanged, and a JSON manifest is printed to standard output.

Use `--rev-include '**/*.css' --rev-include '**/*.js'` to revise only matching
source-relative paths, or `--rev-exclude '**/*.map'` to omit matching paths.
When both are set, exclusion wins. Excluded files stay in `assets` and do not
appear in `assets-rev` or the manifest. Site references to them remain
unchanged; relative references from revisioned assets to excluded files become
same-origin `/assets/...` URLs so they still resolve after the referring asset
moves.

Asset-to-asset reference rewriting applies only to configured file extensions,
independently of which files are revisioned. For example,
`--rewrite-ext .css --rewrite-ext .txt` rewrites references inside CSS and TXT
assets and replaces the default extension list. Generated site-file rewriting
is unchanged.

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

All settings can be set via CLI flag or a config file. Both `--name` and `-name`
flag forms work (including `--name=value`); put flags before SITEROOT. Precedence is
**flag > config file > embedded base config**.

### Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--cdn` | `""` | CDN base URL |
| `--src` | `assets` | Source asset directory (relative to SITEROOT) |
| `--dest` | `assets-rev` | Destination directory for revisioned assets |
| `--rev-include` | _all files_ | Repeatable source-relative glob, e.g. `--rev-include '**/*.css'`; `--rev-include ''` revises none |
| `--rev-exclude` | _no exclusions_ | Repeatable source-relative glob excluded from revisioning, e.g. `--rev-exclude '**/*.map'`; `--rev-exclude ''` clears configured exclusions |
| `--rewrite-ext` | `.css`, `.html`, `.js`, `.json`, `.mjs`, `.svg`, `.toml`, `.webmanifest`, `.xml` | Repeatable asset extension whose references are rewritten before hashing; `--rewrite-ext ''` disables asset-to-asset rewriting |
| `--external-hosts` | `""` (disabled) | Comma-separated exact HTTPS hostnames eligible for local asset downloads; `--external-hosts ''` clears a configured list |
| `--google-fonts` | `false` | Self-host Google Fonts stylesheets and their fonts; `--google-fonts=false` overrides a config file |
| `--config` | _auto_ | Path to config file; use `-` to disable auto-discovery |

### Config file

If `--config` is not given, cdnware looks in SITEROOT then CWD for, in order:
`cdnware.toml`, `cdnware.yaml`, `cdnware.yml`, `cdnware.json`. First match wins.

[`cdnware.example.toml`](cdnware.example.toml) is embedded in the binary at
build time and loaded first. Settings omitted from a site config inherit its
values, even when `--config -` disables auto-discovery. Edit the example and
rebuild to change the shipped defaults; use a site config or flags for local
overrides. The embedded file leaves `rev_include` unset because the default
is to revise every file; `rev_include = []` would instead revise nothing.
It leaves `rev_exclude` unset because the default is to exclude nothing.

```toml
# cdnware.toml
cdn = "https://cdn.example.com/v3"
src = "assets"
dest = "assets-rev"
rev_include = ["**/*.css", "**/*.js", "images/logo-*.svg", "lib/.cdnware/**"]
rev_exclude = ["**/*.map"]
rewrite_extensions = [".css", ".js", ".txt"]
external_hosts = ["cdn.jsdelivr.net"]
google_fonts = true
```

```yaml
# cdnware.yml
cdn: https://cdn.example.com/v3
src: assets
dest: assets-rev
rev_include: ["**/*.css", "**/*.js", "images/logo-*.svg", "lib/.cdnware/**"]
rev_exclude: ["**/*.map"]
rewrite_extensions: [".css", ".js", ".txt"]
external_hosts: [cdn.jsdelivr.net]
google_fonts: true
```

```json
{
  "cdn": "https://cdn.example.com/v3",
  "src": "assets",
  "dest": "assets-rev",
  "rev_include": ["**/*.css", "**/*.js", "images/logo-*.svg", "lib/.cdnware/**"],
  "rev_exclude": ["**/*.map"],
  "rewrite_extensions": [".css", ".js", ".txt"],
  "external_hosts": ["cdn.jsdelivr.net"],
  "google_fonts": true
}
```

With the shipped base config, omitting `rev_include` revises every regular
source file, including files without an extension. An explicit empty list
(`rev_include = []` in TOML, `rev_include: []` in YAML, or
`"rev_include": []` in JSON) revises nothing: the manifest is `{}` and no
destination assets are written. The CLI equivalent is `--rev-include ''`.
Omitting `rev_exclude` (or setting it to an empty list) excludes nothing;
`--rev-exclude ''` clears exclusions from the config file. Exclusion takes
priority over inclusion. Flags replace the corresponding file list rather than
adding to it. This replaces `rev_ext` and `-rev-ext`; migrate existing
selections to source-relative globs such as `**/*.css`.

`rewrite_extensions` is a case-insensitive list of extensions starting with
`.`. It controls which source assets have references rewritten **before**
hashing; it does not filter assets from revisioning. A configured list or
`--rewrite-ext` replaces the entire default list, not just one extension.
An empty list (or `--rewrite-ext ''`) disables asset-to-asset rewriting while
still revisioning files. Generated site files use their existing fixed set of
textual extensions and are not affected.

Both include and exclude patterns match paths relative to `src`, with `/`
separators and case-insensitive matching. `*` matches within one path segment,
`**` matches zero or more directories (so `**/*.css` includes root-level CSS),
and `?`, character classes, and `{one,two}` alternatives are supported. Quote
glob flags to prevent shell expansion. Absolute paths, `.` or `..` segments,
malformed patterns, and duplicates (ignoring case) are errors within each list.

### Self-hosting external assets

Opt in with `--external-hosts cdn.jsdelivr.net` or set
`external_hosts = ["cdn.jsdelivr.net"]` in a site config. For example:

```
cdnware --cdn https://cdn.example.com --external-hosts cdn.jsdelivr.net _site
```

If `_site/index.html` has a `<script src="https://cdn.jsdelivr.net/npm/jquery@3.6.4/dist/jquery.min.js">`,
cdnware downloads it before revisioning, then rewrites `src` to its content-hashed
URL under `https://cdn.example.com/assets-rev/lib/.cdnware/`. The JSON manifest
includes `https://cdn.jsdelivr.net/npm/jquery@3.6.4/dist/jquery.min.js` mapped
to that final URL, as well as the local `/assets/lib/.cdnware/...` source key.
The managed source files and their bookkeeping live under `assets/lib/.cdnware/`;
leave that directory intact between runs. Re-running against an already rewritten
site retains referenced managed files; regenerating the site prunes unused ones.
Unrelated files elsewhere in `assets/lib/` are untouched.

Only HTTPS resources on exact listed hostnames qualify. The HTML scanner covers
`script src`, stylesheet `link href`, image `src`/`srcset`; the CSS scanner covers
`url(...)` and `@import` in CSS files, including recursively downloaded stylesheets.
Remote stylesheet-relative dependencies resolve against the stylesheet's URL.
Navigation links, inline CSS, JavaScript imports, and resources on other hosts
remain external. Redirects must also stay on allowed HTTPS hosts. Each response
is limited to 16 MiB and each request to 15 seconds; at most 128 external files
are downloaded per run. Eligible fetch failures fail the command before page
references are changed. Query-distinct URLs get distinct managed files.

The existing `rev_include`, `rev_exclude`, and `rewrite_extensions` settings still
apply: excluding a downloaded file leaves its final reference at its local
source URL, and disabling CSS rewriting prevents dependency URLs inside
revisioned stylesheets from being localized.

### Self-hosting Google Fonts

Opt in with `--google-fonts` or `google_fonts = true` in TOML (also supported
as a boolean in YAML/JSON). This is independent of `external_hosts`: no
general-host allowlist entry is needed, and enabling it permits only HTTPS
stylesheets from `fonts.googleapis.com` and the `fonts.gstatic.com` font files
referenced by those stylesheets. Redirects must remain on the appropriate
origin. It does not download inline CSS, navigation links, HTTP links, or
unrelated external resources.

Generated HTML stylesheet links and `@import` in generated and local CSS
qualify, including CSS2 family/variant query strings. The stylesheet and every
referenced font are saved in `assets/lib/.cdnware/` (or `<src>/lib/.cdnware/`),
revisioned under `<dest>/lib/.cdnware/`, and mapped from their original HTTPS
URLs to their final same-origin or `--cdn` URLs in the JSON manifest. Font-face
rules and unicode ranges come from Google's CSS response; only URL references
are changed. Google Fonts responses may vary with the request's user agent:
the emitted CSS contains exactly the variants provided at build time, not
synthetic fallbacks for other clients. Check browser compatibility against the
build environment before deploying.

Google Fonts-only preconnect hints are removed from localized HTML unless the
page still contains a Google Fonts origin reference or custom asset selection
and CSS rewriting leave font loads unlocalized. Source CSS/HTML is not modified;
only its revisioned copy is rewritten. The normal `rev_include`, `rev_exclude`,
and `rewrite_extensions` settings still apply: an excluded download stays at
its local source URL, while disabling `.css` rewriting leaves its dependency
URLs unchanged and may prevent complete localization. With
default settings a rerun retains still-referenced managed files and removes
stale ones after site regeneration; unrelated library files are untouched.
Each response is limited to 16 MiB, each request to 15 seconds, and at most
128 managed external files are downloaded per run (shared with
`external_hosts`). Fetch failures stop the run before generated page links
are changed. Review the license for each font family you redistribute and
include any required license or attribution notice with your deployment;
downloaded CSS and binaries alone are not a license review.

## Philosophy

The command keeps one interface for the full deployment transformation: point it at the generated site. It inventories the asset tree, resolves the asset-reference graph, writes content-addressed files, and rewrites generated references. Cyclic asset references fail the build because mutually content-addressed filenames cannot be stable.
