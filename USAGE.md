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
cdn_base_url = "https://cdn.example.com"
asset_prefix = "example/prod/assets-rev/"

[takeoff.dev]
directory = "./_site-dev"
cdn_base_url = "https://cdn.example.com"
asset_prefix = "example/dev/assets-rev/"
```

The default target name is `default`; every integrated target must exist and
provide its own `cdn_base_url` and `asset_prefix`. A named target never inherits
another target's directory or namespace. An omitted directory is `./_site`,
matching Takeoff. Paths in target tables are relative to the selected project
file, not the process working directory. Absolute target directories are also
supported. Shared `src` and `dest` default to `assets` and `assets-rev`; they
must be nonempty relative directories inside that output, with no parent
components. Leading `./`, repeated separators and trailing `/` normalize away.
Takeoff derives its default CDN routing prefix from the same normalized `dest`
plus `/`; its explicit routing overrides remain authoritative for routing.
The namespace prefix is independent of the local destination directory.

`cdn_base_url` is an HTTPS origin without credentials, path, query or fragment
(an optional trailing slash is removed). `asset_prefix` is an unescaped relative
path ending in `/`, without empty/dot components, backslashes, query or fragment.
Shared `[cdnware]` accepts `src`, `dest`, `rev_include`, `rev_exclude`,
`rewrite_extensions`, `external_hosts`, and `google_fonts`; it does not accept
standalone `cdn`. Unknown transformation/target fields and malformed typed data
are errors; unrelated project tables are ignored without logging their values.

Explicit transformation flags still replace configured values. A positional
SITEROOT overrides the selected target directory and remains relative to CWD;
`--cdn` overrides the selected CDN origin, not its asset prefix. Integrated
path/namespace validation still applies to overrides. Missing target hints are
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

The shared resolver supplies `Settings.Namespace` (`CDNBaseURL`, `AssetPrefix`)
and the normalized local paths to the revisioner. Namespace selection is
implemented here; canonical full-SHA256 public naming and URL encoding belong
to the separate canonical-naming change. Current local revisioned basenames
retain cdnware's short-MD5 naming and are not yet a Takeoff URL identity guarantee.
