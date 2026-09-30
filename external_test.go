package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func externalSettings(t *testing.T, root string) Settings {
	t.Helper()
	settings, err := loadSettings([]string{"-config", "-", "-external-hosts", "127.0.0.1", root})
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestExternalAssetsResolveAndReviseCSSGraph(t *testing.T) {
	requests := make(map[string]int)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.RequestURI()]++
		switch r.URL.Path {
		case "/app.js":
			fmt.Fprint(w, "console.log('local');")
		case "/style.css":
			fmt.Fprint(w, `@import "./nested.css?v=1"; /* url(https://127.0.0.1/ignored) */ body { background: url("./icon.svg?v=1"); filter: url("#filter"); }`)
		case "/nested.css":
			fmt.Fprint(w, `@font-face { src: url('./font.woff?v=1'); }`)
		case "/icon.svg":
			fmt.Fprint(w, "<svg/>")
		case "/font.woff":
			fmt.Fprint(w, "font")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	js := server.URL + "/app.js"
	css := server.URL + "/style.css"
	icon := server.URL + "/icon.svg?v=1"
	writeTestFile(t, filepath.Join(root, "index.html"), `<a href="`+js+`">download</a><script src="`+js+`"></script><link rel="alternate stylesheet" href="`+css+`"><img srcset="`+icon+` 1x, `+server.URL+`/icon.svg?v=2 2x"><img src="https://example.com/other.png">`)
	writeTestFile(t, filepath.Join(root, "assets/site.css"), `main { background: url("`+icon+`"); }`)
	writeTestFile(t, filepath.Join(root, "assets/template.html"), `<script src="`+js+`"></script>`)
	writeTestFile(t, filepath.Join(root, "styles.css"), `p { background: url('`+icon+`'); }`)
	settings := externalSettings(t, root)
	settings.Cdn = "https://cdn.example.com/static"
	manifest, err := run(settings, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{js, css, icon, server.URL + "/icon.svg?v=2", server.URL + "/nested.css?v=1", server.URL + "/font.woff?v=1"} {
		final := manifest[remote]
		if !strings.HasPrefix(final, settings.Cdn+"/assets-rev/lib/.cdnware/") {
			t.Fatalf("%s mapped to %q", remote, final)
		}
		local := strings.TrimPrefix(final, settings.Cdn)
		assertFilenameMatchesContent(t, destinationPath(t, root, local))
	}
	if manifest[icon] == manifest[server.URL+"/icon.svg?v=2"] {
		t.Fatal("query variants share a destination")
	}
	page := readTestFile(t, filepath.Join(root, "index.html"))
	if !strings.Contains(page, `<a href="`+js+`">`) || !strings.Contains(page, manifest[js]) || !strings.Contains(page, manifest[css]) || !strings.Contains(page, "https://example.com/other.png") {
		t.Fatalf("wrong HTML references: %s", page)
	}
	style := readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest[css], settings.Cdn)))
	if !strings.Contains(style, manifest[server.URL+"/nested.css?v=1"]) || !strings.Contains(style, manifest[icon]) || !strings.Contains(style, `url("#filter")`) || !strings.Contains(style, "ignored") {
		t.Fatalf("wrong CSS dependencies: %s", style)
	}
	if !strings.Contains(readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest[server.URL+"/nested.css?v=1"], settings.Cdn))), manifest[server.URL+"/font.woff?v=1"]) {
		t.Fatal("nested imported stylesheet did not use revised font URL")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(root, "assets/site.css")), icon) || !strings.Contains(readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest["/assets/site.css"], settings.Cdn))), manifest[icon]) {
		t.Fatal("source CSS changed or revisioned CSS did not localize remote image")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(root, "assets/template.html")), js) || !strings.Contains(readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest["/assets/template.html"], settings.Cdn))), manifest[js]) {
		t.Fatal("source HTML changed or revisioned HTML did not localize script")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(root, "styles.css")), manifest[icon]) || requests["/ignored"] != 0 {
		t.Fatal("site CSS did not localize image or CSS comment triggered download")
	}
	again, err := run(settings, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if again[js] != manifest[js] || again[css] != manifest[css] || !strings.Contains(readTestFile(t, filepath.Join(root, "index.html")), again[js]) {
		t.Fatal("repeat run changed or broke localized URLs")
	}
	writeTestFile(t, filepath.Join(root, "assets/site.css"), "body {}")
	writeTestFile(t, filepath.Join(root, "assets/template.html"), "no assets")
	writeTestFile(t, filepath.Join(root, "styles.css"), "body {}")
	writeTestFile(t, filepath.Join(root, "index.html"), `<a href="`+js+`">download</a>`)
	unrelated := filepath.Join(root, "assets/lib/my-file.js")
	writeTestFile(t, unrelated, "unrelated")
	if _, err := run(settings, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets/lib/.cdnware", managedName(js))); !os.IsNotExist(err) {
		t.Fatalf("stale managed file survived: %v", err)
	}
	if readTestFile(t, unrelated) != "unrelated" {
		t.Fatal("unrelated lib file was changed")
	}
}

func TestExternalAssetsUseRedirectedCSSBaseAndCustomDirectories(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/style.css":
			http.Redirect(w, r, "/packages/ui/main.css", http.StatusFound)
		case "/packages/ui/main.css":
			fmt.Fprint(w, `@import "./base.css"; body { background: url("./icon.svg"); }`)
		case "/packages/ui/base.css":
			fmt.Fprint(w, "body {}")
		case "/packages/ui/icon.svg":
			fmt.Fprint(w, "<svg/>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	remote := server.URL + "/style.css"
	writeTestFile(t, filepath.Join(root, "index.html"), `<link rel="stylesheet" href="`+remote+`">`)
	writeTestFile(t, filepath.Join(root, "static/site.css"), `@import "`+remote+`";`)
	settings := externalSettings(t, root)
	settings.Src, settings.Dest = "static", "public"
	manifest, err := run(settings, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	final := manifest[remote]
	if !strings.HasPrefix(final, "/public/lib/.cdnware/") {
		t.Fatalf("unexpected custom destination %q", final)
	}
	content := readTestFile(t, destinationPath(t, root, final))
	if !strings.Contains(content, manifest[server.URL+"/packages/ui/base.css"]) || !strings.Contains(content, manifest[server.URL+"/packages/ui/icon.svg"]) {
		t.Fatalf("redirected stylesheet did not resolve relative dependencies: %s", content)
	}
	if !strings.Contains(readTestFile(t, filepath.Join(root, "static/site.css")), remote) {
		t.Fatal("source stylesheet was modified")
	}
}

func TestExternalAssetsDoNotOverwriteUnrelatedManagedFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "remote")
	}))
	defer server.Close()
	root := t.TempDir()
	remote := server.URL + "/app.js"
	page := `<script src="` + remote + `"></script>`
	writeTestFile(t, filepath.Join(root, "index.html"), page)
	collision := filepath.Join(root, "assets/lib/.cdnware", managedName(remote))
	writeTestFile(t, collision, "owned by the site")
	if _, err := run(externalSettings(t, root), server.Client()); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected collision error, got %v", err)
	}
	if readTestFile(t, collision) != "owned by the site" || readTestFile(t, filepath.Join(root, "index.html")) != page {
		t.Fatal("collision damaged an unrelated file or site reference")
	}
}

func TestExternalAssetsRejectFailuresBeforeLocalization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle http.HandlerFunc
		want   string
	}{
		{"status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad", 503) }, "503"},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxExternalBytes+1))
		}, "exceeds"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com/private", http.StatusFound)
		}, "not allowed"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { time.Sleep(80 * time.Millisecond); fmt.Fprint(w, "late") }, "deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(tc.handle)
			defer server.Close()
			root := t.TempDir()
			page := `<script src="` + server.URL + `/app.js"></script>`
			writeTestFile(t, filepath.Join(root, "index.html"), page)
			writeTestFile(t, filepath.Join(root, "assets/app.js"), "local")
			client := server.Client()
			if tc.name == "timeout" {
				client.Timeout = 20 * time.Millisecond
			}
			_, err := run(externalSettings(t, root), client)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), server.URL) {
				t.Fatalf("expected %s error with URL, got %v", tc.want, err)
			}
			if got := readTestFile(t, filepath.Join(root, "index.html")); got != page {
				t.Fatalf("page was partially localized: %s", got)
			}
			if _, err := os.Stat(filepath.Join(root, "assets/lib/.cdnware")); !os.IsNotExist(err) {
				t.Fatalf("failed download left managed files: %v", err)
			}
		})
	}
}

func TestExternalHostsConfigurationAndDisabledBehavior(t *testing.T) {
	for _, tc := range []struct{ file, config string }{
		{"cdnware.toml", `external_hosts = ["cdn.example.com"]`},
		{"cdnware.yml", "external_hosts: [cdn.example.com]"},
		{"cdnware.json", `{"external_hosts":["cdn.example.com"]}`},
	} {
		t.Run(tc.file, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, tc.file), tc.config)
			settings, err := loadSettings([]string{root})
			if err != nil || len(settings.ExternalHosts) != 1 || settings.ExternalHosts[0] != "cdn.example.com" {
				t.Fatalf("file hosts: %+v, %v", settings.ExternalHosts, err)
			}
			settings, err = loadSettings([]string{"-external-hosts", "127.0.0.1, OTHER.example.com", root})
			if err != nil || len(settings.ExternalHosts) != 2 || settings.ExternalHosts[1] != "other.example.com" {
				t.Fatalf("flag override: %+v, %v", settings.ExternalHosts, err)
			}
			settings, err = loadSettings([]string{"-external-hosts", "", root})
			if err != nil || len(settings.ExternalHosts) != 0 {
				t.Fatalf("empty override: %+v, %v", settings.ExternalHosts, err)
			}
			writeTestFile(t, filepath.Join(root, "index.html"), `<script src="https://cdn.example.com/app.js"></script>`)
			writeTestFile(t, filepath.Join(root, "assets/icon.svg"), "icon")
			manifest, err := run(settings, nil)
			if err != nil || manifest["/assets/icon.svg"] == "" || strings.Contains(readTestFile(t, filepath.Join(root, "index.html")), "assets-rev/lib") == true {
				t.Fatalf("disabled behavior: %v %+v", err, manifest)
			}
		})
	}
	for _, host := range []string{"cdn.example.com.evil.test", "https://cdn.example.com", "*.example.com", "cdn.example.com:443"} {
		if _, err := parseExternalHosts([]string{host}); err == nil && host != "cdn.example.com.evil.test" {
			t.Fatalf("accepted invalid allowlist entry %q", host)
		}
	}
}

func TestCLIExternalHostDownloadsIntoManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "console.log('external')")
	}))
	defer server.Close()
	root := t.TempDir()
	remote := server.URL + "/app.js"
	writeTestFile(t, filepath.Join(root, "index.html"), `<script src="`+remote+`"></script>`)
	writeTestFile(t, filepath.Join(root, "assets/local.js"), "local")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	certPath := filepath.Join(root, "trusted.pem")
	writeTestFile(t, certPath, string(certificate))

	command := exec.Command("go", "run", ".", "-config", "-", "-external-hosts", "127.0.0.1", root)
	command.Env = append(os.Environ(), "SSL_CERT_FILE="+certPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI run failed: %v: %s", err, output)
	}
	var manifest map[string]string
	if err := json.Unmarshal(output, &manifest); err != nil {
		t.Fatalf("invalid CLI manifest: %s: %v", output, err)
	}
	final := manifest[remote]
	if !strings.HasPrefix(final, "/assets-rev/lib/.cdnware/") || !strings.Contains(readTestFile(t, filepath.Join(root, "index.html")), final) {
		t.Fatalf("CLI did not publish downloaded asset: %s, %s", final, readTestFile(t, filepath.Join(root, "index.html")))
	}
	assertFilenameMatchesContent(t, destinationPath(t, root, final))
}
