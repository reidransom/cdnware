package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test server serves both Google origins while the production URL policy
// and redirect handling still see their real hostnames.
func googleTestClient(server *httptest.Server) *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Local test certificate has no Google DNS names.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}}
}

func TestGoogleFontsSelfHostingAndManagedLifecycle(t *testing.T) {
	requests := make(map[string]int)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.Host+r.URL.RequestURI()]++
		switch {
		case r.Host == "fonts.googleapis.com" && r.URL.Path == "/css2":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			fmt.Fprint(w, `/* latin */ @font-face { font-family: 'Demo'; font-style: normal; font-weight: 400; font-display: swap; src: url(https://fonts.gstatic.com/s/demo/latin.woff2) format('woff2'); unicode-range: U+0000-00FF; }
/* greek */ @font-face { font-family: 'Demo'; font-style: italic; font-weight: 700; font-display: swap; src: url(https://fonts.gstatic.com/s/demo/greek.woff2) format('woff2'); unicode-range: U+0370-03FF; }`)
		case r.Host == "fonts.gstatic.com" && strings.HasSuffix(r.URL.Path, ".woff2"):
			w.Header().Set("Content-Type", "font/woff2")
			fmt.Fprint(w, "wOF2"+r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	style := "https://fonts.googleapis.com/css2?family=Demo:wght@400;700&display=swap"
	alternate := "https://fonts.googleapis.com/css2?family=Demo:ital,wght@1,700&display=swap"
	font1 := "https://fonts.gstatic.com/s/demo/latin.woff2"
	font2 := "https://fonts.gstatic.com/s/demo/greek.woff2"
	page := `<link rel="preconnect" href="https://fonts.googleapis.com"><link href="https://fonts.gstatic.com" rel="preconnect" crossorigin><link rel="preconnect" href="https://other.example.com"><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Demo:wght@400;700&amp;display=swap">`
	writeTestFile(t, filepath.Join(root, "index.html"), page)
	writeTestFile(t, filepath.Join(root, "site.css"), `@import url("`+style+`");`)
	writeTestFile(t, filepath.Join(root, "static/site.css"), `@import "`+alternate+`";`)
	writeTestFile(t, filepath.Join(root, "static/template.html"), page)
	unrelated := filepath.Join(root, "static/lib/my-file.css")
	writeTestFile(t, unrelated, "body {}")
	settings, err := loadSettings([]string{"-config", "-", "-google-fonts", "-src", "static", "-dest", "public", "-cdn", "https://cdn.example.com/v1", root})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := run(settings, googleTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	styleSource := ""
	for _, remote := range []string{style, alternate, font1, font2} {
		final := manifest[remote]
		if !strings.HasPrefix(final, settings.Cdn+"/public/") || strings.Contains(strings.TrimPrefix(final, settings.Cdn+"/public/"), "/") {
			t.Fatalf("%s mapped to %q", remote, final)
		}
		assertFilenameMatchesContent(t, destinationPath(t, root, strings.TrimPrefix(final, settings.Cdn)))
		matched := false
		for local, target := range manifest {
			if strings.HasPrefix(local, "/static/lib/.cdnware/") && target == final {
				destinationPath(t, root, local)
				if remote == style {
					styleSource = local
				}
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s has no local source mapping", remote)
		}
	}
	if !strings.HasSuffix(manifest[style], ".css") || manifest[font1] == manifest[font2] || requests["fonts.googleapis.com/css2?family=Demo:wght@400;700&display=swap"] != 1 {
		t.Fatalf("stylesheet MIME/variants/dedup wrong: %v", requests)
	}
	if manifest[style] == manifest[alternate] || requests["fonts.googleapis.com/css2?family=Demo:ital,wght@1,700&display=swap"] != 1 {
		t.Fatalf("distinct stylesheet queries collapsed: %v", requests)
	}
	gotPage := readTestFile(t, filepath.Join(root, "index.html"))
	if !strings.Contains(gotPage, manifest[style]) || strings.Contains(gotPage, "fonts.googleapis.com") || strings.Contains(gotPage, "fonts.gstatic.com") || !strings.Contains(gotPage, "https://other.example.com") {
		t.Fatalf("page still loads Google Fonts or lost unrelated preconnect: %s", gotPage)
	}
	gotCSS := readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest[style], settings.Cdn)))
	for _, want := range []string{"font-style: italic", "font-weight: 700", "font-display: swap", "unicode-range: U+0370-03FF", "unicode-range: U+0000-00FF", manifest[font1], manifest[font2]} {
		if !strings.Contains(gotCSS, want) {
			t.Fatalf("stylesheet missing %q: %s", want, gotCSS)
		}
	}
	if strings.Contains(gotCSS, "fonts.gstatic.com") || !strings.Contains(readTestFile(t, filepath.Join(root, "site.css")), manifest[style]) || !strings.Contains(readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest["/static/site.css"], settings.Cdn))), manifest[alternate]) {
		t.Fatal("site and asset CSS did not resolve the local stylesheet and font files")
	}
	if readTestFile(t, filepath.Join(root, "static/site.css")) != `@import "`+alternate+`";` || readTestFile(t, filepath.Join(root, "static/template.html")) != page || strings.Contains(readTestFile(t, destinationPath(t, root, strings.TrimPrefix(manifest["/static/template.html"], settings.Cdn))), "fonts.gstatic.com") {
		t.Fatal("source assets changed or revisioned HTML retained a Google Fonts preconnect")
	}
	again, err := run(settings, googleTestClient(server))
	if err != nil || again[style] != manifest[style] || again[alternate] != manifest[alternate] || again[font2] != manifest[font2] {
		t.Fatalf("repeat run changed URLs: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "index.html"), "no fonts")
	writeTestFile(t, filepath.Join(root, "site.css"), "body {}")
	writeTestFile(t, filepath.Join(root, "static/site.css"), "body {}")
	writeTestFile(t, filepath.Join(root, "static/template.html"), "no fonts")
	if _, err := run(settings, googleTestClient(server)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(styleSource, "/")))); !os.IsNotExist(err) || readTestFile(t, unrelated) != "body {}" {
		t.Fatalf("stale Google resource survived or unrelated library file changed: %v", err)
	}
}

func TestGoogleFontsRequiresWOFF2(t *testing.T) {
	for _, tc := range []struct {
		name, css, font, want string
	}{
		{"browser variant", "", "wOF2font", ""},
		{"legacy stylesheet", `@font-face { src: url(https://fonts.gstatic.com/demo.ttf) format('truetype'); }`, "ttf", "WOFF2"},
		{"invalid binary", "", "not a font", "WOFF2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "fonts.googleapis.com" {
					w.Header().Set("Content-Type", "text/css")
					if tc.css != "" {
						fmt.Fprint(w, tc.css)
					} else if strings.Contains(r.UserAgent(), "Chrome/") {
						fmt.Fprint(w, `@font-face { src: url(https://fonts.gstatic.com/demo.woff2) format('woff2'); }`)
					} else {
						fmt.Fprint(w, `@font-face { src: url(https://fonts.gstatic.com/demo.ttf) format('truetype'); }`)
					}
					return
				}
				w.Header().Set("Content-Type", "font/woff2")
				fmt.Fprint(w, tc.font)
			}))
			defer server.Close()
			root := t.TempDir()
			style := "https://fonts.googleapis.com/css2?family=Demo"
			page := `<link rel="stylesheet" href="` + style + `">`
			writeTestFile(t, filepath.Join(root, "index.html"), page)
			settings, err := loadSettings([]string{"-config", "-", "-google-fonts", root})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := run(settings, googleTestClient(server))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "fonts.") {
					t.Fatalf("expected URL-specific %s error, got %v", tc.want, err)
				}
				if got := readTestFile(t, filepath.Join(root, "index.html")); got != page {
					t.Fatalf("page changed after invalid font: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			font := "https://fonts.gstatic.com/demo.woff2"
			css := readTestFile(t, destinationPath(t, root, manifest[style]))
			if !strings.Contains(css, manifest[font]) || !strings.Contains(css, "format('woff2')") || strings.Contains(css, ".ttf") {
				t.Fatalf("staged CSS is not the browser WOFF2 variant: %s", css)
			}
		})
	}
}

func TestGoogleFontsRejectDisallowedResourcesAndFetchFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle http.HandlerFunc
		want   string
	}{
		{"status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }, "503"},
		{"invalid stylesheet", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, "body {}")
		}, "invalid Google Fonts stylesheet"},
		{"invalid MIME", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>error</html>") }, "text/css"},
		{"external font URL", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, `@font-face { src: url(https://evil.example.com/font.woff2); }`)
		}, "invalid Google Fonts stylesheet"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://fonts.gstatic.com/foreign", http.StatusFound)
		}, "not allowed"},
		{"font redirect", func(w http.ResponseWriter, r *http.Request) {
			if r.Host == "fonts.googleapis.com" {
				w.Header().Set("Content-Type", "text/css")
				fmt.Fprint(w, `@font-face { src: url(https://fonts.gstatic.com/font.woff2); }`)
				return
			}
			http.Redirect(w, r, "https://fonts.googleapis.com/foreign", http.StatusFound)
		}, "not allowed"},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxExternalBytes+1))
		}, "exceeds"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(80 * time.Millisecond)
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, `@font-face { src: url(https://fonts.gstatic.com/font.woff2); }`)
		}, "deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(tc.handle)
			defer server.Close()
			root := t.TempDir()
			page := `<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Demo"><link rel="preconnect" href="https://fonts.gstatic.com">`
			writeTestFile(t, filepath.Join(root, "index.html"), page)
			writeTestFile(t, filepath.Join(root, "assets/local.css"), "body {}")
			settings, err := loadSettings([]string{"-config", "-", "-google-fonts", root})
			if err != nil {
				t.Fatal(err)
			}
			client := googleTestClient(server)
			if tc.name == "timeout" {
				client.Timeout = 20 * time.Millisecond
			}
			_, err = run(settings, client)
			failedURL := "fonts.googleapis.com/css2"
			if tc.name == "font redirect" {
				failedURL = "fonts.gstatic.com/font.woff2"
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), failedURL) {
				t.Fatalf("expected URL-specific %s failure, got %v", tc.want, err)
			}
			if got := readTestFile(t, filepath.Join(root, "index.html")); got != page {
				t.Fatalf("page changed after failed download: %s", got)
			}
			if _, err := os.Stat(filepath.Join(root, "assets/lib/.cdnware")); !os.IsNotExist(err) {
				t.Fatalf("failed download left managed files: %v", err)
			}
		})
	}
}

func TestGoogleFontsConfigurationAndHostIndependence(t *testing.T) {
	for _, tc := range []struct{ file, config string }{
		{"cdnware.toml", "google_fonts = true"},
		{"cdnware.yml", "google_fonts: true"},
		{"cdnware.json", `{"google_fonts":true}`},
	} {
		t.Run(tc.file, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, tc.file), tc.config)
			settings, err := loadSettings([]string{root})
			if err != nil || !settings.GoogleFonts || len(settings.ExternalHosts) != 0 {
				t.Fatalf("config not applied: %+v, %v", settings, err)
			}
			settings, err = loadSettings([]string{"-google-fonts=false", root})
			if err != nil || settings.GoogleFonts {
				t.Fatalf("flag did not disable config: %+v, %v", settings, err)
			}
			writeTestFile(t, filepath.Join(root, "index.html"), `<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Demo">`)
			writeTestFile(t, filepath.Join(root, "assets/local.css"), "body {}")
			if _, err := run(settings, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readTestFile(t, filepath.Join(root, "index.html")), "fonts.googleapis.com") {
				t.Fatal("disabled run localized Google Fonts")
			}
		})
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "index.html"), `<script src="https://fonts.googleapis.com/secret.js"></script><img src="https://fonts.gstatic.com/other.png"><link rel="stylesheet" href="https://other.example.com/css2">`)
	writeTestFile(t, filepath.Join(root, "assets/local.css"), "body {}")
	settings, err := loadSettings([]string{"-config", "-", "-google-fonts", root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(settings, googleTestClient(server)); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(root, "index.html")); !strings.Contains(got, "fonts.gstatic.com/other.png") || !strings.Contains(got, "fonts.googleapis.com/secret.js") || !strings.Contains(got, "other.example.com/css2") {
		t.Fatalf("unrelated URLs were changed: %s", got)
	}
	if requests != 0 {
		t.Fatalf("Google-only opt-in fetched %d unrelated resources", requests)
	}
}

func TestGoogleFontsOptInAfterAllowlistedStylesheet(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "fonts.googleapis.com" {
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, `@font-face {font-family: Demo; src: url(https://fonts.gstatic.com/a.woff2) format('woff2');}`)
		} else {
			w.Header().Set("Content-Type", "font/woff2")
			fmt.Fprint(w, "wOF2font")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	style := "https://fonts.googleapis.com/css2?family=Demo"
	page := filepath.Join(root, "index.html")
	writeTestFile(t, page, `<link rel="stylesheet" href="`+style+`">`)
	writeTestFile(t, filepath.Join(root, "assets/local.css"), "body {}")
	settings, err := loadSettings([]string{"-config", "-", "-external-hosts", "fonts.googleapis.com,fonts.gstatic.com", root})
	if err != nil {
		t.Fatal(err)
	}
	original, err := run(settings, googleTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(original[style], ".bin") {
		t.Fatalf("existing allowlist behavior changed: %s", original[style])
	}
	again, err := run(settings, googleTestClient(server))
	if err != nil || again[style] != original[style] {
		t.Fatalf("existing allowlist rerun changed: %v", err)
	}
	settings.GoogleFonts = true
	updated, err := run(settings, googleTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(updated[style], ".css") || !strings.Contains(readTestFile(t, page), updated[style]) || strings.Contains(readTestFile(t, page), original[style]) {
		t.Fatalf("old stylesheet URL was not upgraded: old %s new %s page %s", original[style], updated[style], readTestFile(t, page))
	}
	if _, err := os.Stat(filepath.Join(root, "assets/lib/.cdnware", managedName(style))); err != nil {
		t.Fatalf("previous managed source was removed: %v", err)
	}
}

func TestGoogleFontsRetainsPreconnectWhenCSSIsNotLocalized(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "fonts.gstatic.com" {
			w.Header().Set("Content-Type", "font/woff2")
			fmt.Fprint(w, "wOF2font")
			return
		}
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `@font-face { src: url(https://fonts.gstatic.com/a.woff2) format('woff2'); }`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name string
		set  func(*Settings)
	}{
		{"excluded stylesheet", func(settings *Settings) { settings.RevExclude = []string{"lib/.cdnware/**"} }},
		{"CSS rewriting disabled", func(settings *Settings) { settings.RewriteExtensions = []string{".html"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			style := "https://fonts.googleapis.com/css2?family=Demo"
			page := `<link rel="preconnect" href="https://fonts.gstatic.com"><link rel="stylesheet" href="` + style + `">`
			writeTestFile(t, filepath.Join(root, "index.html"), page)
			writeTestFile(t, filepath.Join(root, "assets/local.css"), "body {}")
			settings, err := loadSettings([]string{"-config", "-", "-google-fonts", root})
			if err != nil {
				t.Fatal(err)
			}
			tc.set(&settings)
			// The stylesheet still uses remote font URLs in either configuration.
			// Its preconnect must not be removed as if localization were complete.
			_, err = run(settings, googleTestClient(server))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readTestFile(t, filepath.Join(root, "index.html")), `rel="preconnect"`) {
				t.Fatal("removed a preconnect needed by an unlocalized font")
			}
		})
	}
}
