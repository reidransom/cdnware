# Shared-project preparation

`cdnware --target dev` prepares the selected local output using
`CWD/project.toml`. Like Takeoff, it does not search parent directories or use
SITEROOT to discover a project. From another working directory, select the
file explicitly:

```sh
/path/to/cdnware --project '/path/to/site/project.toml' --target dev
```

The shared structure, not a flag, selects integrated preparation:

```toml
[cdnware]
src = "assets"
dest = "assets-rev"
rev_exclude = ["**/*.map"]
external_hosts = []
google_fonts = false

[takeoff.default]
directory = "./_site"
cdn_base_url = "https://cdn.example.com/example/prod/assets-rev/"

[takeoff.dev]
directory = "./_site-dev"
cdn_base_url = "https://cdn.example.com/example/dev/assets-rev/"
bucket_key_prefix = "storage/example/dev/"
```

The default target name is `default`; every integrated target must exist and
provide its own full `cdn_base_url`. A named target never inherits
another target's directory or public URL prefix. An omitted directory is `./_site`,
matching Takeoff. Paths in target tables are relative to the selected project
file, not the process working directory. Absolute target directories are also
supported. Shared `src` and `dest` default to `assets` and `assets-rev`; they
must be nonempty relative directories inside that output, with no parent
components. Leading `./`, repeated separators and trailing `/` normalize away.
Takeoff derives its default local CDN routing prefix from the same normalized
`dest` plus `/`; its `cdn_prefixes` overrides remain authoritative for routing.
`dest` selects local output, not the public URL path. The optional
`bucket_key_prefix` selects a storage prefix in Takeoff only and never changes
prepared URLs.

`cdn_base_url` is a full HTTPS URL with a nonempty path ending in `/`. It cannot
contain credentials, a query, or a fragment. Its path cannot contain empty
interior segments, dot segments, backslashes, or percent-encoded separators.
The former `asset_prefix` setting is no longer accepted; put the public path in
`cdn_base_url` instead.
Shared `[cdnware]` accepts `src`, `dest`, `rev_include`, `rev_exclude`,
`rewrite_extensions`, `external_hosts`, and `google_fonts`; it does not accept
standalone `cdn`. Unknown transformation/target fields and malformed typed data
are errors; unrelated project tables are ignored without logging their values.

Explicit transformation flags still replace configured values. A positional
SITEROOT overrides the selected target directory and remains relative to CWD;
`--cdn` replaces the entire selected public URL prefix and must satisfy the
same URL validation. Missing target hints are
errors even when flags are supplied. Prefer target configuration for normal
preparation so it matches Takeoff's selected output.

An auto-selected shared project takes precedence over standalone configuration.
An explicit `--config FILE` selects the existing standalone TOML/YAML/JSON
behavior instead; `--config -` disables all discovery. Without a shared
`[cdnware]` table, standalone discovery remains SITEROOT then CWD, with the
existing flag > file > embedded-default precedence. `--project` and `--config`
cannot be combined; `--target` requires shared configuration.

Preparation reads no token environment variable or credential file, performs
no Dash/policy lookup, and creates no upload/storage reservation. Local assets
need no network. External acquisition remains separately opt-in through
`external_hosts` or `google_fonts` (and their existing flags); enabling those
capabilities can make their documented external requests, never policy calls.

These local hints are not upload authorization. Takeoff/Dash still select the
authenticated server policy; operators maintain agreement with that policy.
No alignment check, build hook, preparation invocation, or rewriting is added
to Takeoff. Build and prepare a clean target-specific tree before deploying.

Integrated preparation appends the URL-encoded flat revisioned basename to
`cdn_base_url`. For example, `app.13fa797c.js` becomes
`https://cdn.example.com/example/dev/assets-rev/app.13fa797c.js` for the dev
target above. There is no additional digest in the public URL or local filename.

In the prefix path and basename, ASCII letters and digits, `-._~`, and path
separators remain literal; every other UTF-8 byte uses an uppercase `%HH`
escape. Fragments stay on references, not in object identities. Nested CSS/JS
and configured textual assets are rewritten before their emitted bytes are
hashed for the eight-hex-character MD5 revision. Cycles fail preparation,
discard failed destination output, and leave site references untouched.
Excluded assets retain same-origin references; `_emails` private components
never enter the revision map.
Standalone configuration retains its existing short-MD5 public naming.

Final prepared bytes are the bytes uploaded. Subsequent source edits require
a clean rebuild and preparation; neither upload nor publication corrects URLs.
If two builds produce different bytes with the same short revision hash and
basename, Takeoff can silently reuse an older stored object unless you deploy
with `--checksum`.

For the explicit cross-tool contract test, build this repository locally with
`go build -o .scratch/cdnware .`, then run from the Takeoff checkout:

```sh
CDNWARE_BIN=/absolute/path/to/cdnware/.scratch/cdnware \
  cargo test --offline -p deploy-core --test cdnware_contract -- --ignored
```

This opt-in test needs the other tool's binary, not credentials or services.
It compares actual generated references and final files with Takeoff core's
inventory for isolated dev/prod policies; normal standalone suites need no
cross-repository tool installation.

The matching Dash checkout's `scripts/test_prepared_publication.sh` adds an
actual Jigyll → cdnware → Takeoff → Dash/Caddy browser proof. Set `CDNWARE_BIN`
to this repository's built executable. Its temporary shared project uses local
trusted TLS asset URLs, isolated dev/prod outputs and synthetic credentials;
preparation runs with no network or tokens. The loopback-only harness measures
browser asset GETs separately from deployment traffic and deliberately proves
that wrong existing provider bytes are accepted by publication.
