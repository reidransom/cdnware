package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func destinationPath(t *testing.T, root, url string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(url, "/")))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected destination %s: %v", path, err)
	}
	return path
}

func assertFilenameMatchesContent(t *testing.T, path string) {
	t.Helper()
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	separator := strings.LastIndexByte(stem, '.')
	if separator == -1 {
		t.Fatalf("revisioned filename has no hash: %s", name)
	}
	want := hashFile(path)
	if got := stem[separator+1:]; got != want {
		t.Fatalf("filename hash %s does not match content hash %s for %s", got, want, path)
	}
}

func TestHashFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "asset.txt")
	writeTestFile(t, path, "hello world")

	first := hashFile(path)
	second := hashFile(path)
	if len(first) != hashLength {
		t.Fatalf("hash length = %d, want %d", len(first), hashLength)
	}
	if first != second {
		t.Fatalf("hash is not deterministic: %s != %s", first, second)
	}

	writeTestFile(t, path, "changed")
	if changed := hashFile(path); changed == first {
		t.Fatal("different content produced the same hash")
	}
}

func TestRevPreservesNestedPathsAndRevisionsEveryFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/images/patrons/person.webp"), "patron")
	writeTestFile(t, filepath.Join(root, "assets/images/team/person.webp"), "team")
	writeTestFile(t, filepath.Join(root, "assets/js/runtime.wasm"), "wasm")
	writeTestFile(t, filepath.Join(root, "assets/data/catalog.bin"), "binary")

	manifest := rev(root, "", "assets", "assets-rev")
	if len(manifest) != 4 {
		t.Fatalf("manifest entries = %d, want 4", len(manifest))
	}

	patronURL := manifest["/assets/images/patrons/person.webp"]
	teamURL := manifest["/assets/images/team/person.webp"]
	if patronURL == teamURL {
		t.Fatalf("same basenames in different directories collided at %s", patronURL)
	}
	if !strings.HasPrefix(patronURL, "/assets-rev/images/patrons/person.") {
		t.Fatalf("patron URL did not preserve directories: %s", patronURL)
	}
	if !strings.HasPrefix(teamURL, "/assets-rev/images/team/person.") {
		t.Fatalf("team URL did not preserve directories: %s", teamURL)
	}
	if !strings.HasSuffix(manifest["/assets/js/runtime.wasm"], ".wasm") {
		t.Fatal("wasm asset was not revisioned")
	}
	if !strings.Contains(manifest["/assets/data/catalog.bin"], ".bin") {
		t.Fatal("unlisted binary extension was not revisioned")
	}

	for _, url := range manifest {
		assertFilenameMatchesContent(t, destinationPath(t, root, url))
	}
}

func TestRevRewritesAssetGraphBeforeHashing(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/js/app.js"), `import "./lib/helper.js";
const wasm = "/assets/js/lib/runtime.wasm";
`)
	writeTestFile(t, filepath.Join(root, "assets/js/lib/helper.js"), `export const value = 1;
`)
	writeTestFile(t, filepath.Join(root, "assets/js/lib/runtime.wasm"), "wasm")

	manifest := rev(root, "https://cdn.example.com/static", "assets", "assets-rev")
	appPath := destinationPath(t, root, strings.TrimPrefix(manifest["/assets/js/app.js"], "https://cdn.example.com/static"))
	content, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	app := string(content)
	if !strings.Contains(app, manifest["/assets/js/lib/helper.js"]) {
		t.Fatalf("relative dependency was not rewritten: %s", app)
	}
	if !strings.Contains(app, manifest["/assets/js/lib/runtime.wasm"]) {
		t.Fatalf("absolute dependency was not rewritten: %s", app)
	}
	assertFilenameMatchesContent(t, appPath)
}

func TestRevRejectsAssetReferenceCycles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/a.js"), `import "./b.js";`)
	writeTestFile(t, filepath.Join(root, "assets/b.js"), `import "./a.js";`)

	revisioner, err := newRevisioner(root, "", "assets", "assets-rev", nil, nil, []string{".js"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = revisioner.revise()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected asset reference cycle error, got %v", err)
	}
}

func TestRevClearsStaleDestinationFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/app.js"), "first")
	stale := filepath.Join(root, "assets-rev/app.stale.js")
	writeTestFile(t, stale, "stale")

	rev(root, "", "assets", "assets-rev")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale destination survived revisioning: %v", err)
	}
}

func TestUsemanRewritesHTMLJavaScriptAndSrcsetReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/images/small.webp"), "small")
	writeTestFile(t, filepath.Join(root, "assets/images/large.webp"), "large")
	writeTestFile(t, filepath.Join(root, "assets/js/app.js"), "app")
	indexPath := filepath.Join(root, "index.html")
	writeTestFile(t, indexPath, `<source srcset="/assets/images/small.webp 480w, /assets/images/large.webp 960w">
<script>new URL("/assets/js/app.js", document.baseURI)</script>`)

	manifest := rev(root, "", "assets", "assets-rev")
	if err := useman(manifest, root, "assets", "assets-rev"); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	result := string(content)
	for sourceURL, revisionedURL := range manifest {
		if strings.Contains(result, sourceURL) {
			t.Fatalf("source URL was not replaced: %s", sourceURL)
		}
		if !strings.Contains(result, revisionedURL) {
			t.Fatalf("revisioned URL is missing: %s", revisionedURL)
		}
	}
}

func TestUsemanLeavesSourceAssetsUnchanged(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "assets/app.js")
	writeTestFile(t, sourcePath, `const icon = "/assets/icon.svg";`)
	writeTestFile(t, filepath.Join(root, "assets/icon.svg"), `<svg/>`)

	manifest := rev(root, "", "assets", "assets-rev")
	if err := useman(manifest, root, "assets", "assets-rev"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "/assets/icon.svg") {
		t.Fatal("source asset was modified")
	}
}

func TestPublicURL(t *testing.T) {
	if got := publicURL("", "assets-rev/app.12345678.js"); got != "/assets-rev/app.12345678.js" {
		t.Fatalf("same-origin URL = %s", got)
	}
	if got := publicURL("https://cdn.example.com/root/", "assets-rev/app.12345678.js"); got != "https://cdn.example.com/root/assets-rev/app.12345678.js" {
		t.Fatalf("CDN URL = %s", got)
	}
}

func TestSourceAndDestinationMustDiffer(t *testing.T) {
	root := t.TempDir()
	if _, err := newRevisioner(root, "", "assets", "assets", nil, nil, nil); err == nil {
		t.Fatal("expected matching source and destination directories to fail")
	}
}

func TestGetUsage(t *testing.T) {
	usage := getUsage()
	if !strings.Contains(usage, "cdnware") || !strings.Contains(usage, "SITEROOT") {
		t.Fatalf("unexpected usage: %s", usage)
	}
}

func TestLoadConfigTOML(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cdnware.toml")
	writeTestFile(t, path, `cdn = "https://x.example.com"
src = "static"
dest = "static-rev"
`)

	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cdn == nil || *cfg.Cdn != "https://x.example.com" {
		t.Errorf("cdn mismatch: %v", cfg.Cdn)
	}
	if cfg.Src == nil || *cfg.Src != "static" {
		t.Errorf("src mismatch: %v", cfg.Src)
	}
	if cfg.Dest == nil || *cfg.Dest != "static-rev" {
		t.Errorf("dest mismatch: %v", cfg.Dest)
	}
}

func TestLoadConfigYAML(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cdnware.yml")
	writeTestFile(t, path, `cdn: https://y.example.com
src: static
dest: static-rev
`)

	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cdn == nil || *cfg.Cdn != "https://y.example.com" {
		t.Errorf("cdn mismatch: %v", cfg.Cdn)
	}
	if cfg.Src == nil || *cfg.Src != "static" {
		t.Errorf("src mismatch: %v", cfg.Src)
	}
	if cfg.Dest == nil || *cfg.Dest != "static-rev" {
		t.Errorf("dest mismatch: %v", cfg.Dest)
	}
}

func TestLoadConfigJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cdnware.json")
	writeTestFile(t, path, `{
  "cdn": "https://j.example.com",
  "src": "static",
  "dest": "static-rev"
}`)

	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cdn == nil || *cfg.Cdn != "https://j.example.com" {
		t.Errorf("cdn mismatch: %v", cfg.Cdn)
	}
	if cfg.Src == nil || *cfg.Src != "static" {
		t.Errorf("src mismatch: %v", cfg.Src)
	}
	if cfg.Dest == nil || *cfg.Dest != "static-rev" {
		t.Errorf("dest mismatch: %v", cfg.Dest)
	}
}

func TestConfigDiscoveryOrder(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cdnware.toml"), `cdn = "from-toml"`)
	writeTestFile(t, filepath.Join(root, "cdnware.json"), `{"cdn": "from-json"}`)

	if path := discoverConfig(root); !strings.HasSuffix(path, "cdnware.toml") {
		t.Errorf("expected cdnware.toml to win, got %s", path)
	}
}

func TestConfigDiscoveryNone(t *testing.T) {
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if path := discoverConfig(root); path != "" {
		t.Errorf("expected no config discovered, got %s", path)
	}
}

func TestLoadSettingsEndToEnd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cdnware.toml")
	writeTestFile(t, path, `cdn = "https://cfg.example.com"
src = "static"
dest = "static-rev"
`)

	settings, err := loadSettings([]string{"-config", path, "-cdn", "https://flag.example.com", root})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Cdn != "https://flag.example.com" {
		t.Errorf("flag cdn should win: %q", settings.Cdn)
	}
	if settings.Src != "static" {
		t.Errorf("src from file: %q", settings.Src)
	}
	if settings.Dest != "static-rev" {
		t.Errorf("dest from file: %q", settings.Dest)
	}
}

func runCLI(t *testing.T, args ...string) (map[string]string, string, error) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(output), err
	}
	var manifest map[string]string
	if err := json.Unmarshal(output, &manifest); err != nil {
		t.Fatalf("invalid manifest %q: %v", output, err)
	}
	return manifest, string(output), nil
}

func TestCLIUsesEmbeddedConfigAsFallback(t *testing.T) {
	root := t.TempDir()
	buildDir := filepath.Join(root, "build")
	for _, name := range []string{"cdnware.go", "go.mod", "go.sum"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(buildDir, name), string(content))
	}
	writeTestFile(t, filepath.Join(buildDir, "cdnware.example.toml"), `cdn = "https://base.example"
src = "media"
dest = "media-rev"
rev_include = ["**/*.css"]
rewrite_extensions = [".css"]
`)
	binary := filepath.Join(root, "cdnware")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = buildDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building with changed base config: %v: %s", err, output)
	}

	site := filepath.Join(root, "site")
	writeTestFile(t, filepath.Join(site, "media/app.css"), "body {}")
	writeTestFile(t, filepath.Join(site, "media/icon.png"), "pixels")
	for _, tc := range []struct {
		name, config, cdn, asset string
		flags                    []string
	}{
		{"base without user config", "-", "https://base.example", "/media/app.css", nil},
		{"partial user config", filepath.Join(site, "cdnware.toml"), "https://site.example", "/media/app.css", nil},
		{"flag overrides base selection", "-", "https://base.example", "/media/icon.png", []string{"-rev-include", "**/*.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config != "-" {
				writeTestFile(t, tc.config, `cdn = "https://site.example"`)
			}
			args := append([]string{"-config", tc.config}, tc.flags...)
			output, err := exec.Command(binary, append(args, site)...).CombinedOutput()
			if err != nil {
				t.Fatalf("running with embedded defaults: %v: %s", err, output)
			}
			var manifest map[string]string
			if err := json.Unmarshal(output, &manifest); err != nil {
				t.Fatalf("invalid manifest %q: %v", output, err)
			}
			if len(manifest) != 1 || !strings.HasPrefix(manifest[tc.asset], tc.cdn+"/media-rev/") {
				t.Fatalf("embedded selection and overrides: %v", manifest)
			}
		})
	}
}

func TestCLISelectsAssetGlobsAndPreservesExcludedReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/css/app.CSS"), `@import "./base.css"; background: url("../images/icon.PNG");`)
	writeTestFile(t, filepath.Join(root, "assets/css/base.css"), `body { color: blue; }`)
	writeTestFile(t, filepath.Join(root, "assets/js/app.JS"), `const img = "/assets/images/icon.PNG";`)
	writeTestFile(t, filepath.Join(root, "assets/images/icon.PNG"), "pixels")
	writeTestFile(t, filepath.Join(root, "assets/images/logo-mark.svg"), "<svg/>")
	writeTestFile(t, filepath.Join(root, "assets/images/other.svg"), "<svg/>")
	index := filepath.Join(root, "index.html")
	writeTestFile(t, index, `<link href="/assets/css/app.CSS"><script src="/assets/js/app.JS"></script><img src="/assets/images/icon.PNG">`)

	manifest, output, err := runCLI(t, "-config", "-", "-cdn", "https://cdn.example.com", "-rev-include", "**/*.css", "-rev-include", "**/*.js", "-rev-include", "images/{logo-*,badge}.svg", root)
	if err != nil {
		t.Fatalf("cdnware: %v: %s", err, output)
	}
	if len(manifest) != 4 || manifest["/assets/css/app.CSS"] == "" || manifest["/assets/css/base.css"] == "" || manifest["/assets/js/app.JS"] == "" || manifest["/assets/images/logo-mark.svg"] == "" {
		t.Fatalf("selected manifest = %v", manifest)
	}
	if _, ok := manifest["/assets/images/icon.PNG"]; ok {
		t.Fatalf("excluded PNG in manifest: %v", manifest)
	}
	if _, ok := manifest["/assets/images/other.svg"]; ok {
		t.Fatalf("nonmatching SVG in manifest: %v", manifest)
	}
	cssPath := filepath.Join(root, strings.TrimPrefix(manifest["/assets/css/app.CSS"], "https://cdn.example.com/"))
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), manifest["/assets/css/base.css"]) || !strings.Contains(string(css), `url("/assets/images/icon.PNG")`) {
		t.Fatalf("revisioned CSS references = %q", css)
	}
	assertFilenameMatchesContent(t, cssPath)
	jsPath := filepath.Join(root, strings.TrimPrefix(manifest["/assets/js/app.JS"], "https://cdn.example.com/"))
	js, err := os.ReadFile(jsPath)
	if err != nil || string(js) != `const img = "/assets/images/icon.PNG";` {
		t.Fatalf("revisioned JS = %q, %v", js, err)
	}
	site, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(site), manifest["/assets/css/app.CSS"]) || !strings.Contains(string(site), manifest["/assets/js/app.JS"]) || !strings.Contains(string(site), `/assets/images/icon.PNG`) {
		t.Fatalf("site references = %q", site)
	}
	entries, err := os.ReadDir(filepath.Join(root, "assets-rev/images"))
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "logo-mark.") {
		t.Fatalf("nonmatching assets were emitted: entries=%v err=%v", entries, err)
	}
}

func TestCLIExcludesAssetsAndPreservesReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/css/app.css"), `@import "./base.css"; background: url("../images/icon.png"); /*# sourceMappingURL=./app.css.map */`)
	writeTestFile(t, filepath.Join(root, "assets/css/base.css"), "body {}")
	writeTestFile(t, filepath.Join(root, "assets/css/app.css.map"), "map")
	writeTestFile(t, filepath.Join(root, "assets/images/icon.png"), "pixels")
	writeTestFile(t, filepath.Join(root, "assets/images/photo.png"), "photo")
	index := filepath.Join(root, "index.html")
	writeTestFile(t, index, `<link href="/assets/css/app.css"><a href="/assets/css/app.css.map"><img src="/assets/images/icon.png">`)

	manifest, output, err := runCLI(t, "-config", "-", "-rev-exclude", "css/base.css", "-rev-exclude", "images/*.PNG", "-rev-exclude", "**/*.map", root)
	if err != nil {
		t.Fatalf("cdnware: %v: %s", err, output)
	}
	if len(manifest) != 1 || manifest["/assets/css/app.css"] == "" {
		t.Fatalf("excluded assets in manifest: %v", manifest)
	}
	css, err := os.ReadFile(destinationPath(t, root, manifest["/assets/css/app.css"]))
	if err != nil || string(css) != `@import "/assets/css/base.css"; background: url("/assets/images/icon.png"); /*# sourceMappingURL=/assets/css/app.css.map */` {
		t.Fatalf("revisioned CSS = %q, err=%v", css, err)
	}
	site, err := os.ReadFile(index)
	if err != nil || string(site) != `<link href="`+manifest["/assets/css/app.css"]+`"><a href="/assets/css/app.css.map"><img src="/assets/images/icon.png">` {
		t.Fatalf("site = %q, err=%v", site, err)
	}
}

func TestCLIConfiguresAssetRewriteExtensions(t *testing.T) {
	for _, tc := range []struct {
		name, filename, config  string
		flags                   []string
		rewriteText, rewriteCSS bool
	}{
		{name: "default", flags: []string{"--config", "-"}, rewriteCSS: true},
		{name: "toml", filename: "cdnware.toml", config: `rewrite_extensions = [".TXT"]`, rewriteText: true},
		{name: "yaml", filename: "cdnware.yaml", config: "rewrite_extensions:\n  - .TXT\n", rewriteText: true},
		{name: "json", filename: "cdnware.json", config: `{"rewrite_extensions":[".TXT"]}`, rewriteText: true},
		{name: "empty file", filename: "cdnware.toml", config: `rewrite_extensions = []`},
		{name: "flag overrides file", filename: "cdnware.toml", config: `rewrite_extensions = [".txt"]`, flags: []string{"--rewrite-ext", ".CSS"}, rewriteCSS: true},
		{name: "empty flag", filename: "cdnware.toml", config: `rewrite_extensions = [".txt"]`, flags: []string{"--rewrite-ext", ""}},
		{name: "repeated flags", flags: []string{"--config", "-", "--rewrite-ext", ".TXT", "--rewrite-ext", ".CSS"}, rewriteText: true, rewriteCSS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/icon.png"), "pixels")
			writeTestFile(t, filepath.Join(root, "assets/app.TXT"), `ref=/assets/icon.png`)
			writeTestFile(t, filepath.Join(root, "assets/app.CSS"), `ref=/assets/icon.png`)
			if tc.filename != "" {
				writeTestFile(t, filepath.Join(root, tc.filename), tc.config)
			}
			manifest, output, err := runCLI(t, append(tc.flags, root)...)
			if err != nil {
				t.Fatalf("cdnware: %v: %s", err, output)
			}
			if len(manifest) != 3 {
				t.Fatalf("unselected assets: %v", manifest)
			}
			for _, file := range []struct {
				name    string
				rewrite bool
			}{
				{"app.TXT", tc.rewriteText},
				{"app.CSS", tc.rewriteCSS},
			} {
				assetPath := destinationPath(t, root, manifest["/assets/"+file.name])
				content, err := os.ReadFile(assetPath)
				if err != nil {
					t.Fatal(err)
				}
				want := "ref=/assets/icon.png"
				if file.rewrite {
					want = "ref=" + manifest["/assets/icon.png"]
				}
				if string(content) != want {
					t.Fatalf("%s content = %q, want %q", file.name, content, want)
				}
				assertFilenameMatchesContent(t, assetPath)
			}
		})
	}
}

func TestCLIRejectsInvalidRewriteExtensions(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		flags        []string
	}{
		{"glob flag", "", []string{"--rewrite-ext", "*.txt"}},
		{"missing dot", "", []string{"--rewrite-ext", "txt"}},
		{"empty with another extension", "", []string{"--rewrite-ext", ".css", "--rewrite-ext", ""}},
		{"duplicate ignoring case", "", []string{"--rewrite-ext", ".CSS", "--rewrite-ext", ".css"}},
		{"path in config", `rewrite_extensions = ["foo/bar"]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/icon.png"), "pixels")
			if tc.config != "" {
				writeTestFile(t, filepath.Join(root, "cdnware.toml"), tc.config)
			}
			_, output, err := runCLI(t, append(tc.flags, root)...)
			if err == nil || !strings.Contains(output, "rewrite extension") {
				t.Fatalf("expected invalid rewrite extension, got %v: %s", err, output)
			}
		})
	}
}

func TestCLIConfigSelectionsAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, config string
	}{
		{"toml", `rev_include = ["**/*.PNG"]` + "\n" + `rev_exclude = ["pic.PNG"]`},
		{"yaml", "rev_include:\n  - '**/*.PNG'\nrev_exclude:\n  - 'pic.PNG'\n"},
		{"json", `{"rev_include":["**/*.PNG"],"rev_exclude":["pic.PNG"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/app.css"), "body {}")
			writeTestFile(t, filepath.Join(root, "assets/pic.PNG"), "pixels")
			writeTestFile(t, filepath.Join(root, "assets/other.PNG"), "other")
			writeTestFile(t, filepath.Join(root, "cdnware."+tc.name), tc.config)
			manifest, output, err := runCLI(t, root)
			if err != nil {
				t.Fatalf("config run: %v: %s", err, output)
			}
			if len(manifest) != 1 || manifest["/assets/other.PNG"] == "" {
				t.Fatalf("file selection and exclusion = %v", manifest)
			}
			manifest, output, err = runCLI(t, "-rev-exclude", "other.png", root)
			if err != nil {
				t.Fatalf("exclude override: %v: %s", err, output)
			}
			if len(manifest) != 1 || manifest["/assets/pic.PNG"] == "" {
				t.Fatalf("flag exclusion = %v", manifest)
			}
			manifest, output, err = runCLI(t, "-rev-exclude", "", root)
			if err != nil {
				t.Fatalf("clear exclusion: %v: %s", err, output)
			}
			if len(manifest) != 2 || manifest["/assets/pic.PNG"] == "" || manifest["/assets/other.PNG"] == "" {
				t.Fatalf("cleared exclusion = %v", manifest)
			}
			manifest, output, err = runCLI(t, "-rev-include", "**/*.CSS", root)
			if err != nil {
				t.Fatalf("flag run: %v: %s", err, output)
			}
			if len(manifest) != 1 || manifest["/assets/app.css"] == "" {
				t.Fatalf("flag selection = %v", manifest)
			}
		})
	}
}

func TestCLIExplicitEmptySelection(t *testing.T) {
	for _, tc := range []struct {
		name, filename, config string
		args                   []string
	}{
		{"toml", "cdnware.toml", `rev_include = []`, nil},
		{"yaml", "cdnware.yml", `rev_include: []`, nil},
		{"json", "cdnware.json", `{"rev_include":[]}`, nil},
		{"flag overrides file", "cdnware.toml", `rev_include = ["**/*.css"]`, []string{"-rev-include", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, tc.filename), tc.config)
			writeTestFile(t, filepath.Join(root, "assets/app.css"), "body {}")
			writeTestFile(t, filepath.Join(root, "assets/pic.png"), "pixels")
			writeTestFile(t, filepath.Join(root, "assets-rev/stale.css"), "stale")
			index := filepath.Join(root, "index.html")
			original := `<link href="/assets/app.css"><img src="/assets/pic.png">`
			writeTestFile(t, index, original)
			manifest, output, err := runCLI(t, append(tc.args, root)...)
			if err != nil || len(manifest) != 0 {
				t.Fatalf("empty selection: manifest=%v err=%v output=%s", manifest, err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "assets-rev")); !os.IsNotExist(err) {
				t.Fatalf("destination remains: %v", err)
			}
			site, err := os.ReadFile(index)
			if err != nil || string(site) != original {
				t.Fatalf("site = %q, err=%v", site, err)
			}
		})
	}
}

func TestCLIRejectsInvalidSelectionGlobs(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		args         []string
	}{
		{"empty extra flag", "", []string{"-rev-include", "**/*.css", "-rev-include", ""}},
		{"absolute flag", "", []string{"-rev-include", "/images/**"}},
		{"empty path component", "", []string{"-rev-include", "images//*.css"}},
		{"duplicate case flag", "", []string{"-rev-include", "**/*.css", "-rev-include", "**/*.CSS"}},
		{"malformed file", `rev_include = ["**/[foo"]`, nil},
		{"parent file", `rev_include = ["../images/**"]`, nil},
		{"duplicate include file", `rev_include = ["**/*.js", "**/*.JS"]`, nil},
		{"absolute exclude flag", "", []string{"-rev-exclude", "/images/**"}},
		{"duplicate exclude flag", "", []string{"-rev-exclude", "**/*.js", "-rev-exclude", "**/*.JS"}},
		{"parent exclude file", `rev_exclude = ["../images/**"]`, nil},
		{"duplicate exclude file", `rev_exclude = ["**/*.js", "**/*.JS"]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/app.css"), "body {}")
			if tc.config != "" {
				writeTestFile(t, filepath.Join(root, "cdnware.toml"), tc.config)
			}
			_, output, err := runCLI(t, append(tc.args, root)...)
			if err == nil || !strings.Contains(output, "rev_") || !strings.Contains(output, "pattern") {
				t.Fatalf("expected invalid glob error, got %v: %s", err, output)
			}
		})
	}
}
