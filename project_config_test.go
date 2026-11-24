package main

import (
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sharedDevPrefix = "https://cdn.development.invalid/site/dev/custom%20prefix/"

const sharedFixture = `
[unrelated]
secret = "unrelated-secret"
[tool.other]
secret = "another-secret"
[cdnware]
src = "./static/input/"
dest = "./static/revised/"
rev_include = ["**/*.js", "**/*.css"]
rev_exclude = ["private/**"]
rewrite_extensions = [".css", ".js"]
external_hosts = []
google_fonts = false
[takeoff.default]
url = "https://production.invalid"
site = "production"
branch = "production"
directory = "./prod output"
api_key_env = "PRODUCTION_TOKEN"
cdn_base_url = "https://cdn.production.invalid/production/prod/assets-rev/"
[takeoff.dev]
url = "https://deployment.invalid"
site = "site"
branch = "main"
directory = "./dev output"
api_key_env = "DEVELOPMENT_TOKEN"
cdn_base_url = "https://cdn.development.invalid/site/dev/custom prefix/"
bucket_key_prefix = "storage/dev/assets-rev/"
`

func writeSharedProject(t *testing.T, content string) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project with spaces")
	path := filepath.Join(root, "project.toml")
	writeTestFile(t, path, content)
	return root, path
}

func TestSharedProjectTargetIsolation(t *testing.T) {
	root, path := writeSharedProject(t, sharedFixture)
	for _, tc := range []struct {
		target, directory, prefix string
	}{
		{"dev", "dev output", sharedDevPrefix},
		{"default", "prod output", "https://cdn.production.invalid/production/prod/assets-rev/"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			settings, err := loadSettings([]string{"--project", path, "--target", tc.target})
			if err != nil {
				t.Fatal(err)
			}
			if settings.BaseDir != filepath.Join(root, tc.directory) || settings.Cdn != tc.prefix || settings.Namespace == nil || settings.Namespace.CDNBaseURL != tc.prefix {
				t.Fatalf("selected settings = %+v, namespace = %+v", settings, settings.Namespace)
			}
			if settings.Src != "static/input" || settings.Dest != "static/revised" || !reflect.DeepEqual(settings.RevInclude, []string{"**/*.js", "**/*.css"}) || !reflect.DeepEqual(settings.RevExclude, []string{"private/**"}) || !reflect.DeepEqual(settings.RewriteExtensions, []string{".css", ".js"}) || len(settings.ExternalHosts) != 0 || settings.GoogleFonts {
				t.Fatalf("transformations = %+v", settings)
			}
		})
	}
	defaults, err := loadSettings([]string{"--project", path})
	if err != nil || defaults.Namespace == nil || defaults.Namespace.CDNBaseURL != "https://cdn.production.invalid/production/prod/assets-rev/" {
		t.Fatalf("default target = %+v: %v", defaults, err)
	}
}

func TestSharedProjectDiscoveryAndExplicitPrecedence(t *testing.T) {
	root, path := writeSharedProject(t, sharedFixture)
	writeTestFile(t, filepath.Join(root, "cdnware.toml"), `cdn = "https://standalone.invalid"`)
	t.Chdir(root)
	settings, err := loadSettings([]string{"--target", "dev"})
	if err != nil || settings.BaseDir != filepath.Join(root, "dev output") || settings.Namespace == nil {
		t.Fatalf("CWD project selection = %+v: %v", settings, err)
	}
	// A selected shared project wins over stale auto-discovered standalone data.
	if settings.Cdn != sharedDevPrefix {
		t.Fatalf("shared CDN = %q", settings.Cdn)
	}
	override := filepath.Join(t.TempDir(), "explicit output")
	settings, err = loadSettings([]string{"--project", path, "--target", "dev", "--src", "./custom input/", "--dest", "./custom output/", "--cdn", "https://override.invalid/new%20prefix%2b/", "--rev-include", "", "--rev-exclude", "", "--rewrite-ext", "", override})
	const overridePrefix = "https://override.invalid/new%20prefix%2B/"
	if err != nil || settings.BaseDir != override || settings.Src != "custom input" || settings.Dest != "custom output" || settings.Cdn != overridePrefix || settings.Namespace == nil || settings.Namespace.CDNBaseURL != overridePrefix || len(settings.RevInclude) != 0 || len(settings.RevExclude) != 0 || len(settings.RewriteExtensions) != 0 {
		t.Fatalf("explicit overrides = %+v: %v", settings, err)
	}
	settings, err = loadSettings([]string{"--config", "cdnware.toml"})
	if err != nil || settings.Namespace != nil || settings.Cdn != "https://standalone.invalid" {
		t.Fatalf("explicit standalone selection = %+v: %v", settings, err)
	}
	settings, err = loadSettings([]string{"--config", "-"})
	if err != nil || settings.Namespace != nil || settings.Cdn != "" {
		t.Fatalf("disabled discovery = %+v: %v", settings, err)
	}
	// Neither a parent project nor a project in SITEROOT is auto-selected.
	subdir := filepath.Join(root, "other working directory")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(subdir)
	settings, err = loadSettings([]string{root})
	if err != nil || settings.Namespace != nil || settings.Cdn != "https://standalone.invalid" {
		t.Fatalf("no ancestor/SITEROOT project discovery = %+v: %v", settings, err)
	}
	settings, err = loadSettings([]string{"--project", "../project.toml", "--target", "dev"})
	if err != nil || settings.BaseDir != filepath.Join(root, "dev output") {
		t.Fatalf("relative explicit project = %+v: %v", settings, err)
	}

	// The new HTTPS prefix requirements apply only to integrated --cdn.
	settings, err = loadSettings([]string{"--config", "-", "--cdn", "http://legacy.invalid/path"})
	if err != nil || settings.Namespace != nil || settings.Cdn != "http://legacy.invalid/path" {
		t.Fatalf("explicit standalone CDN = %+v: %v", settings, err)
	}
}

func TestSharedExternalAcquisitionRemainsExplicit(t *testing.T) {
	_, path := writeSharedProject(t, strings.ReplaceAll(strings.ReplaceAll(sharedFixture, "external_hosts = []", `external_hosts = ["fonts.example"]`), "google_fonts = false", "google_fonts = true"))
	settings, err := loadSettings([]string{"--project", path, "--target", "dev"})
	if err != nil || !reflect.DeepEqual(settings.ExternalHosts, []string{"fonts.example"}) || !settings.GoogleFonts {
		t.Fatalf("explicit external configuration = %+v: %v", settings, err)
	}
	settings, err = loadSettings([]string{"--project", path, "--target", "dev", "--external-hosts", "", "--google-fonts=false"})
	if err != nil || len(settings.ExternalHosts) != 0 || settings.GoogleFonts {
		t.Fatalf("disabled external overrides = %+v: %v", settings, err)
	}
	for _, args := range [][]string{{"--src", "../outside"}, {"--dest", "."}, {"--cdn", ""}} {
		if _, err := loadSettings(append([]string{"--project", path, "--target", "dev"}, args...)); err == nil {
			t.Fatalf("accepted unsafe integrated override %q", args)
		}
	}
}

func TestSharedCDNPrefix(t *testing.T) {
	for _, tc := range []struct{ name, input, canonical string }{
		{"canonical", "https://cdn.example.com/site/prod/assets-rev/", "https://cdn.example.com/site/prod/assets-rev/"},
		{"canonicalize UTF-8 and escapes", "https://cdn.example.com/pub/café%20%2b/%7e/", "https://cdn.example.com/pub/caf%C3%A9%20%2B/~/"},
		{"path punctuation", "https://cdn.example.com/pub/a b+c@%23%3f%25/", "https://cdn.example.com/pub/a%20b%2Bc%40%23%3F%25/"},
		{"HTTP", "http://cdn.example.com/pub/", ""},
		{"missing host", "https:///pub/", ""},
		{"missing path", "https://cdn.example.com", ""},
		{"root only", "https://cdn.example.com/", ""},
		{"missing trailing slash", "https://cdn.example.com/pub", ""},
		{"credentials", "https://user:secret-do-not-log@cdn.example.com/pub/", ""},
		{"query", "https://cdn.example.com/pub/?secret-do-not-log", ""},
		{"empty query", "https://cdn.example.com/pub/?", ""},
		{"fragment", "https://cdn.example.com/pub/#secret-do-not-log", ""},
		{"empty fragment", "https://cdn.example.com/pub/#", ""},
		{"dot segment", "https://cdn.example.com/pub/./assets/", ""},
		{"parent segment", "https://cdn.example.com/pub/../assets/", ""},
		{"encoded parent segment", "https://cdn.example.com/pub/%2e./assets/", ""},
		{"encoded slash", "https://cdn.example.com/pub%2fassets/", ""},
		{"encoded trailing slash", "https://cdn.example.com/pub%2F", ""},
		{"encoded backslash", "https://cdn.example.com/pub%5Cassets/", ""},
		{"literal backslash", `https://cdn.example.com/pub\assets/`, ""},
		{"empty interior segment", "https://cdn.example.com/pub//assets/", ""},
		{"empty first segment", "https://cdn.example.com//pub/", ""},
		{"malformed escape", "https://cdn.example.com/pub/%6/", ""},
		{"invalid UTF-8", "https://cdn.example.com/pub/%FF/", ""},
		{"encoded NUL", "https://cdn.example.com/pub/%00/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := writeSharedProject(t, fmt.Sprintf("[cdnware]\n[takeoff.default]\ncdn_base_url = %q\n", tc.input))
			_, validPath := writeSharedProject(t, sharedFixture)
			for _, args := range [][]string{{"--project", path}, {"--project", validPath, "--cdn", tc.input}} {
				settings, err := loadSettings(args)
				if tc.canonical == "" {
					if err == nil || strings.Contains(err.Error(), "secret-do-not-log") {
						t.Fatalf("expected a redacted error, got %v for %v", err, args)
					}
				} else if err != nil || settings.Namespace == nil || settings.Namespace.CDNBaseURL != tc.canonical || settings.Cdn != tc.canonical {
					t.Fatalf("CDN prefix = %q, want %q: %v", settings.Cdn, tc.canonical, err)
				}
			}
		})
	}
}

func TestSharedProjectRejectsMalformedDataWithoutSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, from, to string
	}{
		{"missing target", "[takeoff.dev]", "[takeoff.other]"},
		{"missing base", "cdn_base_url = \"https://cdn.development.invalid/site/dev/custom prefix/\"", ""},
		{"obsolete prefix", "bucket_key_prefix", "asset_prefix"},
		{"invalid bucket prefix type", "bucket_key_prefix = \"storage/dev/assets-rev/\"", "bucket_key_prefix = 17"},
		{"empty directory", "directory = \"./dev output\"", "directory = \"\""},
		{"invalid target type", "directory = \"./dev output\"", "directory = 17"},
		{"invalid credential type", "api_key_env = \"DEVELOPMENT_TOKEN\"", "api_key_env = 17"},
		{"unknown target field", "branch = \"main\"", "unknown = \"secret-do-not-log\""},
		{"unknown transformation", "google_fonts = false", "unknown = \"secret-do-not-log\""},
		{"shared cdn", "google_fonts = false", "cdn = \"https://wrong.invalid\""},
		{"invalid list", "external_hosts = []", "external_hosts = \"secret-do-not-log\""},
		{"invalid boolean", "google_fonts = false", "google_fonts = \"secret-do-not-log\""},
		{"empty src", "./static/input/", ""},
		{"root dest", "./static/revised/", "/"},
		{"parent src", "./static/input/", "static/../input"},
		{"absolute src", "./static/input/", "/static/input"},
		{"drive src", "./static/input/", "C:input"},
		{"empty base", "https://cdn.development.invalid/site/dev/custom prefix/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := writeSharedProject(t, strings.Replace(sharedFixture, tc.from, tc.to, 1))
			_, err := loadSettings([]string{"--project", path, "--target", "dev"})
			if err == nil {
				t.Fatal("accepted malformed shared configuration")
			}
			for _, secret := range []string{"secret-do-not-log", "unrelated-secret", "another-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("configuration error leaks secret: %v", err)
				}
			}
		})
	}
	for _, content := range []string{
		"cdnware = []\ntakeoff = []",
		"cdnware = \"secret-do-not-log\"",
		"[cdnware]\n[takeoff]\ndev = []",
		"[cdnware]\n[takeoff.dev]\ncdn_base_url = [\"secret-do-not-log\"]",
		"[tool.takeoff]\n[cdnware]\n",
		"not TOML secret-do-not-log",
	} {
		_, path := writeSharedProject(t, content)
		if _, err := loadSettings([]string{"--project", path, "--target", "dev"}); err == nil || strings.Contains(err.Error(), "secret-do-not-log") {
			t.Fatalf("invalid tables = %q: %v", content, err)
		}
	}
}

func TestSharedProjectMissingSourcesAndStandaloneRegressions(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, args := range [][]string{
		{"--target", "dev"},
		{"--target", "default"},
		{"--project", "missing.toml"},
		{"--project", "."},
		{"--project", "project.toml", "--config", "-"},
		{"--config", "-", "--target", "dev"},
	} {
		if _, err := loadSettings(args); err == nil {
			t.Fatalf("accepted unresolved selection %q", args)
		}
	}
	writeTestFile(t, filepath.Join(root, "project.toml"), "[unrelated]\nsecret = \"unrelated-secret\"\n")
	writeTestFile(t, filepath.Join(root, "cdnware.json"), `{"cdn":"https://standalone.invalid","src":"input","dest":"output"}`)
	settings, err := loadSettings(nil)
	if err != nil || settings.Namespace != nil || settings.Cdn != "https://standalone.invalid" || settings.Src != "input" || settings.Dest != "output" {
		t.Fatalf("unrelated project must preserve standalone = %+v: %v", settings, err)
	}
	// Shared defaults agree with Takeoff's default directory and routing.
	writeTestFile(t, filepath.Join(root, "project.toml"), "[cdnware]\n[takeoff.default]\ncdn_base_url = \"https://cdn.invalid/site/default/assets-rev/\"\n")
	settings, err = loadSettings(nil)
	if err != nil || settings.BaseDir != filepath.Join(root, "_site") || settings.Src != "assets" || settings.Dest != "assets-rev" {
		t.Fatalf("shared defaults = %+v: %v", settings, err)
	}
}

type rejectPreparationNetwork struct{ calls int }

func (transport *rejectPreparationNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls++
	return nil, errors.New("network disabled for offline preparation")
}

func TestSharedPreparationNeverReadsCredentialsOrContactsDeployment(t *testing.T) {
	for _, token := range []string{"", "invalid-token", "/inaccessible/credential/file"} {
		t.Run(token, func(t *testing.T) {
			root, path := writeSharedProject(t, sharedFixture)
			t.Setenv("DEVELOPMENT_TOKEN", token)
			t.Setenv("DASH_API_TOKEN", token)
			t.Setenv("HOME", filepath.Join(root, "nonexistent credentials home"))
			dev := filepath.Join(root, "dev output")
			prod := filepath.Join(root, "prod output", "index.html")
			writeTestFile(t, prod, "untouched production")
			writeTestFile(t, filepath.Join(dev, "static/input/app.js"), "console.log('dev');")
			writeTestFile(t, filepath.Join(dev, "index.html"), `<script src="/static/input/app.js"></script><script src="https://external.invalid/remote.js"></script>`)
			settings, err := loadSettings([]string{"--project", path, "--target", "dev"})
			if err != nil {
				t.Fatal(err)
			}
			transport := &rejectPreparationNetwork{}
			manifest, err := run(settings, &http.Client{Transport: transport})
			if err != nil || transport.calls != 0 || len(manifest) != 1 {
				t.Fatalf("offline preparation = %v, requests=%d: %v", manifest, transport.calls, err)
			}
			assetURL := manifest["/static/input/app.js"]
			if !strings.HasPrefix(assetURL, sharedDevPrefix) || strings.Contains(assetURL, "prod") {
				t.Fatalf("dev asset URL = %s", assetURL)
			}
			files, err := os.ReadDir(filepath.Join(dev, "static/revised"))
			if err != nil || len(files) != 1 {
				t.Fatalf("local revisioned files = %v: %v", files, err)
			}
			want := sharedDevPrefix + files[0].Name()
			if assetURL != want {
				t.Fatalf("canonical URL = %s, want %s", assetURL, want)
			}
			if got, err := os.ReadFile(prod); err != nil || string(got) != "untouched production" {
				t.Fatalf("prod was changed: %q: %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(dev, "index.html")); err != nil || !strings.Contains(string(got), assetURL) || !strings.Contains(string(got), "https://external.invalid/remote.js") {
				t.Fatalf("dev HTML = %q: %v", got, err)
			}
		})
	}
}

func TestSharedCanonicalGraphFailureLeavesNoPartialOutput(t *testing.T) {
	root, path := writeSharedProject(t, sharedFixture)
	dev := filepath.Join(root, "dev output")
	const html = `<script src="/static/input/a.js"></script>`
	writeTestFile(t, filepath.Join(dev, "index.html"), html)
	writeTestFile(t, filepath.Join(dev, "static/input/0.bin"), "written before cycle")
	writeTestFile(t, filepath.Join(dev, "static/input/a.js"), `import "./b.js";`)
	writeTestFile(t, filepath.Join(dev, "static/input/b.js"), `import "./a.js";`)
	settings, err := loadSettings([]string{"--project", path, "--target", "dev", "--rev-include", "**/*"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &rejectPreparationNetwork{}
	manifest, err := run(settings, &http.Client{Transport: transport})
	if err == nil || !strings.Contains(err.Error(), "asset reference cycle involving /static/input/a.js") || manifest != nil {
		t.Fatalf("cycle result = %v: %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(dev, "static/revised")); !os.IsNotExist(err) {
		t.Fatalf("partial destination survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dev, "index.html")); err != nil || string(got) != html || transport.calls != 0 {
		t.Fatalf("site changed after cycle: %q, requests=%d: %v", got, transport.calls, err)
	}
}

func TestSharedCanonicalConfiguredTextAndPrivateInputs(t *testing.T) {
	root, path := writeSharedProject(t, sharedFixture)
	dev := filepath.Join(root, "dev output")
	writeTestFile(t, filepath.Join(dev, "static/input/data.txt"), `"/static/input/blob#part"`)
	writeTestFile(t, filepath.Join(dev, "static/input/blob"), "no extension")
	writeTestFile(t, filepath.Join(dev, "static/input/_emails/secret.txt"), "private")
	writeTestFile(t, filepath.Join(dev, "_emails/receipt.html"), "private")
	settings, err := loadSettings([]string{"--project", path, "--target", "dev", "--rev-include", "**/*", "--rewrite-ext", ".txt"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := run(settings, &http.Client{Transport: &rejectPreparationNetwork{}})
	if err != nil || len(manifest) != 2 {
		t.Fatalf("manifest = %v: %v", manifest, err)
	}
	files, err := os.ReadDir(filepath.Join(dev, "static/revised"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(dev, "static/revised", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file.Name(), ".txt") && string(content) != `"`+manifest["/static/input/blob"]+`#part"` {
			t.Fatalf("configured text dependency = %q", content)
		}
		want := sharedDevPrefix + file.Name()
		found := false
		for _, url := range manifest {
			found = found || url == want
		}
		if !found {
			t.Fatalf("revisioned file URL missing: %s", want)
		}
	}
}

func TestSharedPreparationUsesFlatEncodedBasenamesWithOrWithoutBucketOverride(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bucketPrefix string
	}{
		{"default", ""},
		{"override", `bucket_key_prefix = "different/storage/"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := strings.Replace(sharedFixture, `bucket_key_prefix = "storage/dev/assets-rev/"`, tc.bucketPrefix, 1)
			root, path := writeSharedProject(t, fixture)
			dev := filepath.Join(root, "dev output")
			const (
				sourceCSS  = "/static/input/css/nested/app.css"
				sourceIcon = "/static/input/img/é 100%+#.svg"
				icon       = "<svg/>"
			)
			writeTestFile(t, filepath.Join(dev, sourceCSS), `body { background: url("../../img/%C3%A9%20100%25%2B%23.svg#part"); }`)
			writeTestFile(t, filepath.Join(dev, sourceIcon), icon)
			writeTestFile(t, filepath.Join(dev, "index.html"), `<link rel="stylesheet" href="`+sourceCSS+`">`)

			iconRevision := fmt.Sprintf("%x", md5.Sum([]byte(icon)))[:8]
			iconName := "é 100%+#." + iconRevision + ".svg"
			iconURL := sharedDevPrefix + "%C3%A9%20100%25%2B%23." + iconRevision + ".svg"
			css := `body { background: url("` + iconURL + `#part"); }`
			cssRevision := fmt.Sprintf("%x", md5.Sum([]byte(css)))[:8]
			cssName := "app." + cssRevision + ".css"
			cssURL := sharedDevPrefix + cssName

			settings, err := loadSettings([]string{"--project", path, "--target", "dev", "--rev-include", "**/*"})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := run(settings, &http.Client{Transport: &rejectPreparationNetwork{}})
			want := map[string]string{sourceCSS: cssURL, sourceIcon: iconURL}
			if err != nil || !reflect.DeepEqual(manifest, want) {
				t.Fatalf("manifest = %v, want %v: %v", manifest, want, err)
			}
			for name, want := range map[string]string{
				"static/revised/" + cssName:  css,
				"static/revised/" + iconName: icon,
				"index.html":                 `<link rel="stylesheet" href="` + cssURL + `">`,
			} {
				if got := readTestFile(t, filepath.Join(dev, name)); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestCanonicalAssetPathEncoding(t *testing.T) {
	if got, want := encodeAssetPath("a/é 100%+?#[]!$&'()*,;=:@-._~"), "a/%C3%A9%20100%25%2B%3F%23%5B%5D%21%24%26%27%28%29%2A%2C%3B%3D%3A%40-._~"; got != want {
		t.Fatalf("encoding = %s, want %s", got, want)
	}
	for _, path := range []string{"", strings.Repeat("AZaz09-._~/", 4)} {
		var got string
		allocations := testing.AllocsPerRun(100, func() {
			got = encodeAssetPath(path)
		})
		if got != path || allocations != 0 {
			t.Fatalf("unchanged encoding = %q, allocations = %g; want %q with none", got, allocations, path)
		}
	}
}
