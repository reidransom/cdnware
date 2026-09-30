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

	revisioner, err := newRevisioner(root, "", "assets", "assets-rev", nil)
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
	if _, err := newRevisioner(root, "", "assets", "assets", nil); err == nil {
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

func TestMergePrecedence(t *testing.T) {
	cdnFile := "from-file"
	destFile := "file-rev"
	fileCfg := &Config{Cdn: &cdnFile, Dest: &destFile}
	fs := &flagSet{cdn: "from-flag"}

	settings := mergeSettings(".", fileCfg, fs, map[string]bool{"cdn": true})
	if settings.Cdn != "from-flag" {
		t.Errorf("flag should win for cdn: got %q", settings.Cdn)
	}
	if settings.Dest != "file-rev" {
		t.Errorf("file should win for dest: got %q", settings.Dest)
	}
	if settings.Src != defaultSrc {
		t.Errorf("default should win for src: got %q", settings.Src)
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

func TestCLISelectsExtensionsAndPreservesExcludedReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "assets/css/app.CSS"), `@import "./base.css"; background: url("../images/icon.PNG");`)
	writeTestFile(t, filepath.Join(root, "assets/css/base.css"), `body { color: blue; }`)
	writeTestFile(t, filepath.Join(root, "assets/js/app.JS"), `const img = "/assets/images/icon.PNG";`)
	writeTestFile(t, filepath.Join(root, "assets/images/icon.PNG"), "pixels")
	index := filepath.Join(root, "index.html")
	writeTestFile(t, index, `<link href="/assets/css/app.CSS"><script src="/assets/js/app.JS"></script><img src="/assets/images/icon.PNG">`)

	manifest, output, err := runCLI(t, "-config", "-", "-cdn", "https://cdn.example.com", "-rev-ext", ".css,.js", root)
	if err != nil {
		t.Fatalf("cdnware: %v: %s", err, output)
	}
	if len(manifest) != 3 || manifest["/assets/css/app.CSS"] == "" || manifest["/assets/css/base.css"] == "" || manifest["/assets/js/app.JS"] == "" {
		t.Fatalf("selected manifest = %v", manifest)
	}
	if _, ok := manifest["/assets/images/icon.PNG"]; ok {
		t.Fatalf("excluded PNG in manifest: %v", manifest)
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
	if !os.IsNotExist(err) || len(entries) != 0 {
		t.Fatalf("excluded PNG was emitted: entries=%v err=%v", entries, err)
	}
}

func TestCLIConfigSelectionsAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, config string
	}{
		{"toml", `rev_ext = [".PNG"]`},
		{"yaml", "rev_ext:\n  - .PNG\n"},
		{"json", `{"rev_ext":[".PNG"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/app.css"), "body {}")
			writeTestFile(t, filepath.Join(root, "assets/pic.PNG"), "pixels")
			writeTestFile(t, filepath.Join(root, "cdnware."+tc.name), tc.config)
			manifest, output, err := runCLI(t, root)
			if err != nil {
				t.Fatalf("config run: %v: %s", err, output)
			}
			if len(manifest) != 1 || manifest["/assets/pic.PNG"] == "" {
				t.Fatalf("file selection = %v", manifest)
			}
			manifest, output, err = runCLI(t, "-rev-ext", ".CSS", root)
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
		{"toml", "cdnware.toml", `rev_ext = []`, nil},
		{"yaml", "cdnware.yml", `rev_ext: []`, nil},
		{"json", "cdnware.json", `{"rev_ext":[]}`, nil},
		{"flag overrides file", "cdnware.toml", `rev_ext = [".css"]`, []string{"-rev-ext", ""}},
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

func TestCLIRejectsInvalidExtensions(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		args         []string
	}{
		{"missing dot flag", "", []string{"-rev-ext", "css"}},
		{"empty entry flag", "", []string{"-rev-ext", ".css,"}},
		{"duplicate case flag", "", []string{"-rev-ext", ".css,.CSS"}},
		{"malformed file", `rev_ext = [".css", "../js"]`, nil},
		{"duplicate file", `rev_ext = [".js", ".JS"]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "assets/app.css"), "body {}")
			if tc.config != "" {
				writeTestFile(t, filepath.Join(root, "cdnware.toml"), tc.config)
			}
			_, output, err := runCLI(t, append(tc.args, root)...)
			if err == nil || !strings.Contains(output, "rev_ext extension") {
				t.Fatalf("expected extension error, got %v: %s", err, output)
			}
		})
	}
}
