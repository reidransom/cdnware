package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

const (
	maxExternalBytes = 16 << 20
	maxExternalFiles = 128
	externalTimeout  = 15 * time.Second
	// Google Fonts selects font formats from the stylesheet request's user agent.
	googleFontsUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

type managedAsset struct {
	Source       string   `json:"source"`
	Final        string   `json:"final"`
	Digest       string   `json:"digest"`
	Base         string   `json:"base,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type externalAssets struct {
	settings     Settings
	client       *http.Client
	hosts        map[string]bool
	previous     map[string]managedAsset
	active       map[string]managedAsset
	payload      map[string][]byte
	files        map[string][]byte // Generated HTML/CSS, read once before any writes.
	googleStyles bool
	stripHints   bool
}

func parseExternalHosts(entries []string) ([]string, error) {
	hosts := make([]string, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSpace(entry))
		if host == "" {
			if len(entries) == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid external_hosts entry %q: expected a hostname", entry)
		}
		if strings.ContainsAny(host, "/:@?#*\\ \t\r\n") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
			return nil, fmt.Errorf("invalid external_hosts entry %q: expected a literal hostname without scheme or port", entry)
		}
		for _, r := range host {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' {
				return nil, fmt.Errorf("invalid external_hosts entry %q: expected a literal hostname", entry)
			}
		}
		if seen[host] {
			return nil, fmt.Errorf("duplicate external_hosts entry %q", entry)
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func (e *externalAssets) allowed(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && e.hosts[strings.ToLower(u.Hostname())] && u.Hostname() != ""
}

type downloadRole string

const (
	googleStylesheet downloadRole = "google stylesheet"
	googleFont       downloadRole = "google font"
)

type roleContextKey struct{}

func googleOrigin(u *url.URL, host string) bool {
	return u.Scheme == "https" && u.User == nil && strings.EqualFold(u.Hostname(), host) && (u.Port() == "" || u.Port() == "443")
}

func (e *externalAssets) role(u, base *url.URL, css bool) downloadRole {
	if !e.settings.GoogleFonts {
		return ""
	}
	if css && googleOrigin(u, "fonts.googleapis.com") {
		return googleStylesheet
	}
	if !css && base != nil && googleOrigin(base, "fonts.googleapis.com") && googleOrigin(u, "fonts.gstatic.com") {
		return googleFont
	}
	return ""
}

func (e *externalAssets) permitted(u *url.URL, role downloadRole) bool {
	switch role {
	case googleStylesheet:
		return googleOrigin(u, "fonts.googleapis.com")
	case googleFont:
		return googleOrigin(u, "fonts.gstatic.com")
	default:
		return e.allowed(u)
	}
}

// externalURL returns the fetch identity; HTTP resources ignore fragments.
func (e *externalAssets) externalURL(raw string, base *url.URL, css bool) string {
	if base != nil && (raw == "" || strings.HasPrefix(raw, "#")) {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if !e.permitted(u, e.role(u, base, css)) {
		return ""
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

func managedName(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	u, _ := url.Parse(raw)
	ext := filepath.Ext(u.Path)
	if len(ext) > 12 || len(ext) < 2 {
		ext = ".bin"
	} else {
		for _, c := range ext[1:] {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				ext = ".bin"
				break
			}
		}
	}
	return hex.EncodeToString(sum[:]) + ext
}

func googleManagedName(raw string) string {
	name := managedName(raw)
	u, err := url.Parse(raw)
	if err == nil && strings.EqualFold(u.Hostname(), "fonts.googleapis.com") && (u.Path == "/css" || u.Path == "/css2") {
		return strings.TrimSuffix(name, filepath.Ext(name)) + ".css"
	}
	return name
}

func (e *externalAssets) sourceFor(name string) string {
	return "/" + filepath.ToSlash(filepath.Join(e.settings.Src, "lib", ".cdnware", name))
}

func (e *externalAssets) source(raw string) string {
	if old, exists := e.previous[raw]; exists {
		return old.Source
	}
	if e.settings.GoogleFonts {
		return e.sourceFor(googleManagedName(raw))
	}
	return e.sourceFor(managedName(raw))
}

func (e *externalAssets) managedDir() string {
	return filepath.Join(e.settings.BaseDir, e.settings.Src, "lib", ".cdnware")
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// rewriteCSS finds URL tokens without matching text inside CSS comments or
// unrelated quoted strings. It keeps original syntax when replacing values.
func rewriteCSS(content []byte, replace func(string, bool) string) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(content); {
		if i+1 < len(content) && content[i] == '/' && content[i+1] == '*' {
			end := bytes.Index(content[i+2:], []byte("*/"))
			if end < 0 {
				break
			}
			i += end + 4
			continue
		}
		if content[i] == '\'' || content[i] == '"' {
			i = skipCSSString(content, i)
			continue
		}
		isImport := hasCSSWord(content, i, "@import")
		start := i
		if isImport {
			start += len("@import")
			for start < len(content) && isCSSSpace(content[start]) {
				start++
			}
		}
		isURL := hasCSSWord(content, start, "url(")
		if !isImport && !isURL {
			i++
			continue
		}
		valueStart := start
		if isURL {
			valueStart += 4
			for valueStart < len(content) && isCSSSpace(content[valueStart]) {
				valueStart++
			}
		}
		valueEnd := valueStart
		if valueEnd < len(content) && (content[valueEnd] == '\'' || content[valueEnd] == '"') {
			valueEnd = skipCSSString(content, valueStart) - 1
			if valueEnd < valueStart || valueEnd >= len(content) || content[valueEnd] != content[valueStart] {
				i++
				continue
			}
			valueStart++
		} else if isURL {
			for valueEnd < len(content) && content[valueEnd] != ')' && !isCSSSpace(content[valueEnd]) && content[valueEnd] != '\'' && content[valueEnd] != '"' {
				valueEnd++
			}
		} else {
			i++
			continue
		}
		if isURL {
			end := valueEnd
			if valueStart > 0 && (content[valueStart-1] == '\'' || content[valueStart-1] == '"') {
				end++
			}
			for end < len(content) && isCSSSpace(content[end]) {
				end++
			}
			if end >= len(content) || content[end] != ')' {
				i++
				continue
			}
		}
		if valueEnd > valueStart {
			value := string(content[valueStart:valueEnd])
			if replacement := replace(value, isImport); replacement != value {
				if out == nil {
					out = make([]byte, 0, len(content)+len(replacement))
				}
				out = append(out, content[last:valueStart]...)
				out = append(out, replacement...)
				last = valueEnd
			}
		}
		i = valueEnd + 1
	}
	if out == nil {
		return content
	}
	return append(out, content[last:]...)
}

func hasCSSWord(content []byte, pos int, word string) bool {
	if pos+len(word) > len(content) || !bytes.EqualFold(content[pos:pos+len(word)], []byte(word)) {
		return false
	}
	return pos == 0 || !isCSSIdentifier(content[pos-1])
}

func isCSSIdentifier(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func isCSSSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' }

func skipCSSString(content []byte, i int) int {
	quote := content[i]
	for i++; i < len(content); i++ {
		if content[i] == '\\' {
			i++
		} else if content[i] == quote {
			return i + 1
		}
	}
	return len(content)
}

// transformHTML only serializes modified tags; all other bytes are preserved.
func transformHTML(content []byte, replace func(string, bool) string) []byte {
	z := html.NewTokenizer(bytes.NewReader(content))
	var out []byte
	offset := 0
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			break
		}
		raw := z.Raw()
		if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
			if out != nil {
				out = append(out, raw...)
			}
			offset += len(raw)
			continue
		}
		token := z.Token()
		name := strings.ToLower(token.Data)
		stylesheet := false
		if name == "link" {
			for _, attr := range token.Attr {
				if attr.Key == "rel" {
					for _, rel := range strings.Fields(strings.ToLower(attr.Val)) {
						stylesheet = stylesheet || rel == "stylesheet"
					}
				}
			}
		}
		changed := false
		for i := range token.Attr {
			attr := &token.Attr[i]
			switch {
			case name == "script" && attr.Key == "src", name == "img" && attr.Key == "src", name == "link" && stylesheet && attr.Key == "href":
				updated := replace(attr.Val, name == "link")
				changed = changed || updated != attr.Val
				attr.Val = updated
			case (name == "img" || name == "source") && attr.Key == "srcset":
				// A comma inside a data URL is not an external HTTPS URL; preserve it.
				parts := strings.Split(attr.Val, ",")
				for n, part := range parts {
					start := len(part) - len(strings.TrimLeft(part, " \t\r\n"))
					end := start
					for end < len(part) && !isCSSSpace(part[end]) {
						end++
					}
					updated := replace(part[start:end], false)
					changed = changed || updated != part[start:end]
					parts[n] = part[:start] + updated + part[end:]
				}
				attr.Val = strings.Join(parts, ",")
			}
		}
		if changed {
			rendered := token.String()
			if out == nil {
				out = make([]byte, 0, len(content)+len(rendered)-len(raw))
				out = append(out, content[:offset]...)
			}
			out = append(out, rendered...)
		} else if out != nil {
			out = append(out, raw...)
		}
		offset += len(raw)
	}
	if out == nil {
		return content
	}
	return append(out, content[offset:]...)
}

// stripGooglePreconnect keeps the hint if anything on the page still refers
// to a Google Fonts origin outside the hint itself.
func stripGooglePreconnect(content []byte) []byte {
	z := html.NewTokenizer(bytes.NewReader(content))
	var out []byte
	offset := 0
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			break
		}
		raw := z.Raw()
		remove := false
		if typ == html.StartTagToken || typ == html.SelfClosingTagToken {
			token := z.Token()
			if strings.EqualFold(token.Data, "link") {
				preconnect, google := false, false
				for _, attr := range token.Attr {
					switch attr.Key {
					case "rel":
						roles := strings.Fields(strings.ToLower(attr.Val))
						preconnect = len(roles) == 1 && roles[0] == "preconnect"
					case "href":
						u, err := url.Parse(attr.Val)
						google = err == nil && (googleOrigin(u, "fonts.googleapis.com") || googleOrigin(u, "fonts.gstatic.com"))
					}
				}
				remove = preconnect && google
			}
		}
		if remove {
			if out == nil {
				out = make([]byte, 0, len(content))
				out = append(out, content[:offset]...)
			}
		} else if out != nil {
			out = append(out, raw...)
		}
		offset += len(raw)
	}
	if out == nil {
		return content
	}
	out = append(out, content[offset:]...)
	if bytes.Contains(out, []byte("fonts.googleapis.com")) || bytes.Contains(out, []byte("fonts.gstatic.com")) {
		return content
	}
	return out
}

func externalReplacement(raw string, base *url.URL, mapping map[string]string) string {
	if base != nil && (raw == "" || strings.HasPrefix(raw, "#")) {
		return raw
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return raw
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	fragment := ""
	if u.Fragment != "" {
		fragment = "#" + u.EscapedFragment()
	}
	u.Fragment, u.RawFragment = "", ""
	if target, ok := mapping[u.String()]; ok {
		return target + fragment
	}
	return raw
}

func (e *externalAssets) fetch(raw string, css bool, role downloadRole) error {
	if old, ok := e.active[raw]; ok {
		if !css || old.Dependencies != nil {
			return nil
		}
		// The same URL was first discovered as a generic resource, then as CSS.
		base, _ := url.Parse(old.Base)
		return e.fetchCSS(raw, base, e.payload[raw])
	}
	if len(e.active) >= maxExternalFiles {
		return fmt.Errorf("external asset limit of %d exceeded at %s", maxExternalFiles, raw)
	}
	u, err := url.Parse(raw)
	if err != nil || !e.permitted(u, role) {
		return fmt.Errorf("external URL not allowed: %s", raw)
	}
	request, err := http.NewRequestWithContext(context.WithValue(context.Background(), roleContextKey{}, role), http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", raw, err)
	}
	if role == googleStylesheet {
		request.Header.Set("User-Agent", googleFontsUserAgent)
	}
	response, err := e.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", raw, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("fetching %s: HTTP %s", raw, response.Status)
	}
	if role == googleStylesheet {
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || mediaType != "text/css" {
			return fmt.Errorf("fetching %s: expected text/css stylesheet", raw)
		}
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxExternalBytes+1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", raw, err)
	}
	if len(content) > maxExternalBytes {
		return fmt.Errorf("fetching %s: response exceeds %d bytes", raw, maxExternalBytes)
	}
	if role == googleFont && (len(content) == 0 || strings.HasPrefix(response.Header.Get("Content-Type"), "text/html")) {
		return fmt.Errorf("fetching %s: invalid font response", raw)
	}
	if role == googleFont && !bytes.HasPrefix(content, []byte("wOF2")) {
		return fmt.Errorf("fetching %s: expected WOFF2 font", raw)
	}
	e.payload[raw] = content
	e.active[raw] = managedAsset{Source: e.source(raw), Digest: digest(content), Base: response.Request.URL.String()}
	if css {
		return e.fetchCSS(raw, response.Request.URL, content)
	}
	return nil
}

func (e *externalAssets) fetchCSS(raw string, base *url.URL, content []byte) error {
	entry := e.active[raw]
	entry.Dependencies = []string{}
	e.active[raw] = entry // Mark before descending so CSS import cycles terminate here.
	var order []string
	imports := make(map[string]bool)
	google := e.settings.GoogleFonts && googleOrigin(base, "fonts.googleapis.com")
	fonts := 0
	var invalid string
	rewriteCSS(content, func(value string, imported bool) string {
		resolved := e.externalURL(value, base, imported)
		if google {
			expected := googleFont
			if imported {
				expected = googleStylesheet
			}
			u, err := url.Parse(strings.TrimSpace(value))
			if err != nil || resolved == "" || e.role(base.ResolveReference(u), base, imported) != expected ||
				!imported && !strings.EqualFold(filepath.Ext(u.Path), ".woff2") {
				invalid = value
			} else if !imported {
				fonts++
			}
		}
		if resolved != "" {
			if _, seen := imports[resolved]; !seen {
				order = append(order, resolved)
			}
			imports[resolved] = imports[resolved] || imported
		}
		return value
	})
	if google && (invalid != "" || fonts == 0 || !bytes.Contains(bytes.ToLower(content), []byte("@font-face"))) {
		return fmt.Errorf("fetching %s: invalid Google Fonts stylesheet (expected WOFF2 font URLs; invalid URL %q)", raw, invalid)
	}
	entry.Dependencies = order
	e.active[raw] = entry
	for _, dependency := range order {
		depURL, _ := url.Parse(dependency)
		if err := e.fetch(dependency, imports[dependency], e.role(depURL, base, imports[dependency])); err != nil {
			return err
		}
	}
	return nil
}

func (e *externalAssets) scan() error {
	dest := filepath.Clean(filepath.Join(e.settings.BaseDir, e.settings.Dest))
	return filepath.WalkDir(e.settings.BaseDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		clean := filepath.Clean(path)
		if entry.IsDir() && (clean == dest || clean == e.managedDir()) {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".html" && ext != ".css" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		e.files[path] = content
		var fetchErr error
		observe := func(value string, css bool) string {
			if fetchErr != nil {
				return value
			}
			if raw := e.externalURL(value, nil, css); raw != "" {
				u, _ := url.Parse(raw)
				fetchErr = e.fetch(raw, css, e.role(u, nil, css))
			}
			return value
		}
		if ext == ".html" {
			transformHTML(content, observe)
		} else if ext == ".css" {
			rewriteCSS(content, func(value string, imported bool) string { return observe(value, imported) })
		}
		return fetchErr
	})
}

func (e *externalAssets) statePath() string { return filepath.Join(e.managedDir(), "state.json") }

func (e *externalAssets) readState() error {
	for _, parent := range []string{filepath.Join(e.settings.BaseDir, e.settings.Src, "lib"), e.managedDir(), e.statePath()} {
		info, err := os.Lstat(parent)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || parent == e.statePath() && !info.Mode().IsRegular()) {
			return fmt.Errorf("external asset state path is not a regular file/directory: %s", parent)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := os.ReadFile(e.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading external asset state: %w", err)
	}
	if err := json.Unmarshal(data, &e.previous); err != nil {
		return fmt.Errorf("parsing external asset state: %w", err)
	}
	for raw, entry := range e.previous {
		if entry.Source != e.sourceFor(managedName(raw)) && entry.Source != e.sourceFor(googleManagedName(raw)) {
			return fmt.Errorf("invalid external asset state for %s", raw)
		}
	}
	return nil
}

func (e *externalAssets) retainReferenced() {
	var retain func(string)
	retain = func(raw string) {
		if _, exists := e.active[raw]; exists {
			return
		}
		entry, ok := e.previous[raw]
		if !ok {
			return
		}
		e.active[raw] = entry
		for _, dependency := range entry.Dependencies {
			retain(dependency)
		}
	}
	for raw, entry := range e.previous {
		for path, content := range e.files {
			if strings.HasPrefix(filepath.Clean(path), filepath.Clean(filepath.Join(e.settings.BaseDir, e.settings.Src))+string(filepath.Separator)) {
				continue
			}
			if bytes.Contains(content, []byte(entry.Final)) || bytes.Contains(content, []byte(entry.Source)) {
				retain(raw)
				break
			}
		}
	}
}

func (e *externalAssets) stage() error {
	dir := e.managedDir()
	for _, parent := range []string{filepath.Join(e.settings.BaseDir, e.settings.Src, "lib"), dir} {
		info, err := os.Lstat(parent)
		if err == nil && !info.IsDir() {
			return fmt.Errorf("external asset directory is not a directory: %s", parent)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for raw, entry := range e.active {
		if _, downloaded := e.payload[raw]; downloaded {
			continue
		}
		path := filepath.Join(dir, filepath.Base(entry.Source))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("previously downloaded asset is missing or unsafe: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil || digest(content) != entry.Digest {
			return fmt.Errorf("previously downloaded asset has changed: %s", path)
		}
	}
	for raw := range e.payload {
		path := filepath.Join(dir, filepath.Base(e.source(raw)))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		previous, tracked := e.previous[raw]
		if !tracked || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite unrelated or non-regular file %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil || digest(content) != previous.Digest {
			return fmt.Errorf("refusing to overwrite modified file %s", path)
		}
	}
	for raw, entry := range e.previous {
		if _, keep := e.active[raw]; keep {
			continue
		}
		path := filepath.Join(dir, filepath.Base(entry.Source))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil && digest(data) == entry.Digest {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for raw, content := range e.payload {
		path := filepath.Join(dir, filepath.Base(e.source(raw)))
		if err := os.WriteFile(path, content, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (e *externalAssets) canStripGoogleHints(r *revisioner) bool {
	if !e.googleStyles || !r.rewriteExtensions[".css"] {
		return false
	}
	for raw, entry := range e.active {
		u, err := url.Parse(raw)
		if err == nil && googleOrigin(u, "fonts.googleapis.com") && entry.Dependencies != nil {
			if _, selected := r.assets[entry.Source]; !selected {
				return false
			}
		}
	}
	sourceRoot := filepath.Join(e.settings.BaseDir, e.settings.Src)
	for path, content := range e.files {
		if !strings.EqualFold(filepath.Ext(path), ".css") || !bytes.Contains(content, []byte("fonts.googleapis.com")) {
			continue
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		sourceURL := "/" + filepath.ToSlash(filepath.Join(e.settings.Src, rel))
		if _, selected := r.assets[sourceURL]; !selected {
			return false
		}
	}
	return true
}

func (e *externalAssets) finish(manifest map[string]string) error {
	var previousFinals map[string]string
	for raw, entry := range e.active {
		if final, ok := manifest[entry.Source]; ok {
			entry.Final = final
		} else {
			entry.Final = entry.Source // Normal rev_include/rev_exclude selection.
		}
		if old, ok := e.previous[raw]; ok && old.Final != entry.Final {
			if previousFinals == nil {
				previousFinals = make(map[string]string)
			}
			previousFinals[old.Final] = entry.Final
		}
		e.active[raw] = entry
		manifest[raw] = entry.Final
	}
	replacements := manifest
	if len(previousFinals) != 0 {
		replacements = make(map[string]string, len(manifest)+len(previousFinals))
		for old, final := range manifest {
			replacements[old] = final
		}
		for old, final := range previousFinals {
			replacements[old] = final
		}
	}
	googleActive := e.stripHints
	for path := range e.files {
		if strings.HasPrefix(filepath.Clean(path), filepath.Clean(filepath.Join(e.settings.BaseDir, e.settings.Src))+string(filepath.Separator)) {
			continue
		}
		content, err := os.ReadFile(path) // useman may already have rewritten local references.
		if err != nil {
			return err
		}
		var updated []byte
		if strings.EqualFold(filepath.Ext(path), ".css") {
			updated = rewriteCSS(content, func(value string, _ bool) string { return externalReplacement(value, nil, replacements) })
		} else {
			updated = transformHTML(content, func(value string, _ bool) string { return externalReplacement(value, nil, replacements) })
			if googleActive {
				updated = stripGooglePreconnect(updated)
			}
		}
		if !bytes.Equal(content, updated) {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(e.active, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(e.statePath(), data, 0644)
}

func run(settings Settings, client *http.Client) (map[string]string, error) {
	var external *externalAssets
	if len(settings.ExternalHosts) != 0 || settings.GoogleFonts {
		copyClient := *client
		if copyClient.Timeout == 0 || copyClient.Timeout > externalTimeout {
			copyClient.Timeout = externalTimeout
		}
		external = &externalAssets{
			settings: settings, client: &copyClient,
			hosts: make(map[string]bool), previous: make(map[string]managedAsset),
			active: make(map[string]managedAsset), payload: make(map[string][]byte), files: make(map[string][]byte),
		}
		copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			role, _ := req.Context().Value(roleContextKey{}).(downloadRole)
			if len(via) >= 10 || !external.permitted(req.URL, role) {
				return fmt.Errorf("external redirect to %s is not allowed", req.URL)
			}
			return nil
		}
		for _, host := range settings.ExternalHosts {
			external.hosts[host] = true
		}
		if err := external.readState(); err != nil {
			return nil, err
		}
		if err := external.scan(); err != nil {
			return nil, err
		}
		external.retainReferenced()
		if settings.GoogleFonts {
			for raw, entry := range external.active {
				u, err := url.Parse(raw)
				if err == nil && googleOrigin(u, "fonts.googleapis.com") && entry.Dependencies != nil {
					external.googleStyles = true
					break
				}
			}
		}
		if err := external.stage(); err != nil {
			return nil, err
		}
	}
	r, err := newRevisioner(settings.BaseDir, settings.Cdn, settings.Src, settings.Dest, settings.RevInclude, settings.RevExclude, settings.RewriteExtensions)
	if err != nil {
		return nil, err
	}
	if external != nil {
		r.googleStyles = external.googleStyles
		external.stripHints = external.canStripGoogleHints(r)
		r.stripGoogleHints = external.stripHints
		r.external = make(map[string]string, len(external.active))
		for raw, entry := range external.active {
			r.external[raw] = entry.Source
		}
		r.externalBases = make(map[string]*url.URL)
		for _, entry := range external.active {
			if entry.Dependencies != nil {
				r.externalBases[entry.Source], _ = url.Parse(entry.Base)
			}
		}
	}
	manifest, err := r.revise()
	if err != nil {
		return nil, err
	}
	if err := useman(manifest, settings.BaseDir, settings.Src, settings.Dest); err != nil {
		return nil, err
	}
	if external != nil {
		if err := external.finish(manifest); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}
