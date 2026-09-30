package main

import (
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

const hashLength = 8

//go:embed cdnware.example.toml
var defaultConfig []byte

// Config holds file-backed settings. Pointer fields distinguish omitted
// values from explicit empty values.
type Config struct {
	Cdn        *string   `toml:"cdn" yaml:"cdn" json:"cdn,omitempty"`
	Src        *string   `toml:"src" yaml:"src" json:"src,omitempty"`
	Dest       *string   `toml:"dest" yaml:"dest" json:"dest,omitempty"`
	RevInclude *[]string `toml:"rev_include" yaml:"rev_include" json:"rev_include,omitempty"`
	RevExclude *[]string `toml:"rev_exclude" yaml:"rev_exclude" json:"rev_exclude,omitempty"`
}

type Settings struct {
	BaseDir    string
	Cdn        string
	Src        string
	Dest       string
	RevInclude []string
	RevExclude []string
}

type asset struct {
	path string
	rel  string
	url  string
	mode fs.FileMode
}

type revisioner struct {
	baseDir  string
	srcDir   string
	destDir  string
	cdn      string
	assets   map[string]asset
	excluded map[string]string
	state    map[string]uint8
	result   map[string]string
}

func hashReader(reader io.Reader) (string, error) {
	hash := md5.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil))[:hashLength], nil
}

func hashFile(path string) string {
	file, err := os.Open(path)
	check(err)
	defer file.Close()

	hash, err := hashReader(file)
	check(err)
	return hash
}

func hashBytes(content []byte) string {
	hash, err := hashReader(bytes.NewReader(content))
	check(err)
	return hash
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func revisionedName(rel, hash string) string {
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	if ext == "" {
		return stem + "." + hash
	}
	return stem + "." + hash + ext
}

func publicURL(cdn, path string) string {
	path = "/" + strings.TrimLeft(filepath.ToSlash(path), "/")
	if cdn == "" {
		return path
	}
	return strings.TrimRight(cdn, "/") + path
}

func newRevisioner(baseDir, cdn, srcDir, destDir string, revInclude, revExclude []string) (*revisioner, error) {
	if filepath.Clean(srcDir) == filepath.Clean(destDir) {
		return nil, errors.New("source and destination directories must differ")
	}

	r := &revisioner{
		baseDir: baseDir,
		srcDir:  filepath.Clean(srcDir),
		destDir: filepath.Clean(destDir),
		cdn:     cdn,
		assets:  make(map[string]asset),
		state:   make(map[string]uint8),
		result:  make(map[string]string),
	}
	if revInclude != nil || len(revExclude) != 0 {
		r.excluded = make(map[string]string)
	}

	sourceRoot := filepath.Join(baseDir, r.srcDir)
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		url := "/" + filepath.ToSlash(filepath.Join(r.srcDir, rel))
		if revInclude != nil || len(revExclude) != 0 {
			name := strings.ToLower(filepath.ToSlash(rel))
			selected := revInclude == nil
			for _, pattern := range revInclude {
				if doublestar.MatchUnvalidated(pattern, name) {
					selected = true
					break
				}
			}
			if selected {
				for _, pattern := range revExclude {
					if doublestar.MatchUnvalidated(pattern, name) {
						selected = false
						break
					}
				}
			}
			if !selected {
				r.excluded[url] = rel
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		r.assets[url] = asset{path: path, rel: rel, url: url, mode: info.Mode()}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking source assets: %w", err)
	}

	return r, nil
}

func isRewritableAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css", ".html", ".js", ".json", ".mjs", ".svg", ".toml", ".webmanifest", ".xml":
		return true
	default:
		return false
	}
}

// referenceIndex ignores matches that are prefixes of longer asset paths.
func referenceIndex(content, url []byte) int {
	for offset := 0; offset < len(content); {
		index := bytes.Index(content[offset:], url)
		if index < 0 {
			break
		}
		index += offset
		end := index + len(url)
		if end == len(content) || !isAssetPathByte(content[end]) {
			return index
		}
		offset = end
	}
	return -1
}

func isAssetPathByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b >= 0x80 ||
		b == '.' || b == '_' || b == '-' || b == '/' || b == '~' || b == '%'
}

func replaceAssetReference(content, old, target []byte) []byte {
	var result []byte
	start := 0
	for start < len(content) {
		index := referenceIndex(content[start:], old)
		if index < 0 {
			break
		}
		index += start
		if result == nil {
			result = make([]byte, 0, len(content)+len(target))
		}
		result = append(result, content[start:index]...)
		result = append(result, target...)
		start = index + len(old)
	}
	if result == nil {
		return content
	}
	return append(result, content[start:]...)
}

type replacement struct {
	old string
	url string
}

func (r *revisioner) dependencies(current asset, content []byte) []replacement {
	replacements := make([]replacement, 0)
	currentDir := filepath.Dir(current.rel)

	for sourceURL, dependency := range r.assets {
		if dependency.url == current.url {
			continue
		}
		if referenceIndex(content, []byte(sourceURL)) >= 0 {
			replacements = append(replacements, replacement{old: sourceURL, url: dependency.url})
		}

		rel, err := filepath.Rel(currentDir, dependency.rel)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		if referenceIndex(content, []byte(rel)) >= 0 {
			replacements = append(replacements, replacement{old: rel, url: dependency.url})
		}
	}
	for sourceURL, relPath := range r.excluded {
		rel, err := filepath.Rel(currentDir, relPath)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		if referenceIndex(content, []byte(rel)) >= 0 {
			replacements = append(replacements, replacement{old: rel, url: sourceURL})
		}
	}

	sort.Slice(replacements, func(i, j int) bool {
		return len(replacements[i].old) > len(replacements[j].old)
	})
	return replacements
}

func (r *revisioner) writeAsset(sourceURL string) (string, error) {
	if r.state[sourceURL] == 2 {
		return r.result[sourceURL], nil
	}
	if r.state[sourceURL] == 1 {
		return "", fmt.Errorf("asset reference cycle involving %s", sourceURL)
	}

	current, ok := r.assets[sourceURL]
	if !ok {
		return "", fmt.Errorf("unknown asset %s", sourceURL)
	}
	r.state[sourceURL] = 1

	content, err := os.ReadFile(current.path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", current.path, err)
	}
	if isRewritableAsset(current.path) {
		for _, replacement := range r.dependencies(current, content) {
			dependencyURL := replacement.url
			if _, selected := r.assets[replacement.url]; selected {
				dependencyURL, err = r.writeAsset(replacement.url)
				if err != nil {
					return "", err
				}
			}
			content = replaceAssetReference(content, []byte(replacement.old), []byte(dependencyURL))
		}
	}

	hash := hashBytes(content)
	destRel := revisionedName(current.rel, hash)
	destPath := filepath.Join(r.baseDir, r.destDir, destRel)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", fmt.Errorf("creating destination directory: %w", err)
	}
	if err := os.WriteFile(destPath, content, current.mode.Perm()); err != nil {
		return "", fmt.Errorf("writing %s: %w", destPath, err)
	}

	url := publicURL(r.cdn, filepath.Join(r.destDir, destRel))
	r.result[sourceURL] = url
	r.state[sourceURL] = 2
	return url, nil
}

func (r *revisioner) revise() (map[string]string, error) {
	destination := filepath.Join(r.baseDir, r.destDir)
	if err := os.RemoveAll(destination); err != nil {
		return nil, fmt.Errorf("clearing destination: %w", err)
	}

	urls := make([]string, 0, len(r.assets))
	for url := range r.assets {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	for _, url := range urls {
		if _, err := r.writeAsset(url); err != nil {
			return nil, err
		}
	}
	return r.result, nil
}

func rev(baseDir, cdn, srcDir, destDir string) map[string]string {
	revisioner, err := newRevisioner(baseDir, cdn, srcDir, destDir, nil, nil)
	check(err)
	manifest, err := revisioner.revise()
	check(err)
	return manifest
}

func isReferenceSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css", ".html", ".js", ".json", ".mjs", ".toml", ".webmanifest", ".xml":
		return true
	default:
		return false
	}
}

func repFile(path string, manifest map[string]string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	original := content

	keys := make([]string, 0, len(manifest))
	for sourceURL := range manifest {
		keys = append(keys, sourceURL)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, sourceURL := range keys {
		content = replaceAssetReference(content, []byte(sourceURL), []byte(manifest[sourceURL]))
	}
	if bytes.Equal(content, original) {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, info.Mode().Perm())
}

func useman(manifest map[string]string, baseDir, srcDir, destDir string) error {
	sourceRoot := filepath.Clean(filepath.Join(baseDir, srcDir))
	destinationRoot := filepath.Clean(filepath.Join(baseDir, destDir))

	return filepath.WalkDir(baseDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		cleanPath := filepath.Clean(path)
		if entry.IsDir() && (cleanPath == sourceRoot || cleanPath == destinationRoot) {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() || !isReferenceSource(path) {
			return nil
		}
		if err := repFile(path, manifest); err != nil {
			return fmt.Errorf("rewriting %s: %w", path, err)
		}
		return nil
	})
}

func loadConfigFile(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	cfg := &Config{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		err = toml.Unmarshal(data, cfg)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, cfg)
	case ".json":
		err = json.Unmarshal(data, cfg)
	default:
		return nil, fmt.Errorf("unknown config format for %s (expected .toml/.yaml/.yml/.json)", path)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg, nil
}

func loadDefaultConfig() (*Config, error) {
	cfg := &Config{}
	if err := toml.Unmarshal(defaultConfig, cfg); err != nil {
		return nil, fmt.Errorf("parsing embedded cdnware.example.toml: %w", err)
	}
	if cfg.Cdn == nil || cfg.Src == nil || cfg.Dest == nil {
		return nil, errors.New("embedded cdnware.example.toml must define cdn, src, and dest")
	}
	return cfg, nil
}

func discoverConfig(siteroot string) string {
	names := []string{"cdnware.toml", "cdnware.yaml", "cdnware.yml", "cdnware.json"}
	dirs := []string{siteroot}
	if cwd, err := os.Getwd(); err == nil && cwd != siteroot {
		dirs = append(dirs, cwd)
	}
	for _, dir := range dirs {
		for _, name := range names {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}

type flagSet struct {
	fs         *flag.FlagSet
	cdn        string
	src        string
	dest       string
	revInclude patternFlags
	revExclude patternFlags
	configPath string
}

func mergeSettings(baseDir string, defaults, fileCfg *Config, fs *flagSet, explicit map[string]bool) (Settings, error) {
	settings := Settings{
		BaseDir: baseDir,
		Cdn:     *defaults.Cdn,
		Src:     *defaults.Src,
		Dest:    *defaults.Dest,
	}
	revInclude, revExclude := defaults.RevInclude, defaults.RevExclude
	if fileCfg != nil {
		if fileCfg.Cdn != nil {
			settings.Cdn = *fileCfg.Cdn
		}
		if fileCfg.Src != nil {
			settings.Src = *fileCfg.Src
		}
		if fileCfg.Dest != nil {
			settings.Dest = *fileCfg.Dest
		}
		if fileCfg.RevInclude != nil {
			revInclude = fileCfg.RevInclude
		}
		if fileCfg.RevExclude != nil {
			revExclude = fileCfg.RevExclude
		}
	}
	if explicit["cdn"] {
		settings.Cdn = fs.cdn
	}
	if explicit["src"] {
		settings.Src = fs.src
	}
	if explicit["dest"] {
		settings.Dest = fs.dest
	}
	if explicit["rev-include"] {
		entries := []string(fs.revInclude)
		if len(entries) == 1 && entries[0] == "" {
			entries = []string{}
		}
		revInclude = &entries
	}
	if explicit["rev-exclude"] {
		entries := []string(fs.revExclude)
		if len(entries) == 1 && entries[0] == "" {
			entries = []string{}
		}
		revExclude = &entries
	}
	if revInclude != nil {
		var err error
		settings.RevInclude, err = parseRevPatterns(*revInclude, "rev_include")
		if err != nil {
			return Settings{}, err
		}
	}
	if revExclude != nil {
		var err error
		settings.RevExclude, err = parseRevPatterns(*revExclude, "rev_exclude")
		if err != nil {
			return Settings{}, err
		}
	}
	return settings, nil
}

type patternFlags []string

func (patterns *patternFlags) String() string {
	return strings.Join(*patterns, ",")
}

func (patterns *patternFlags) Set(pattern string) error {
	*patterns = append(*patterns, pattern)
	return nil
}

func parseRevPatterns(entries []string, option string) ([]string, error) {
	patterns := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		pattern := strings.ToLower(strings.TrimSpace(entry))
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, `\`) || !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("invalid %s pattern %q: expected a relative glob with / separators", option, entry)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("invalid %s pattern %q: expected a relative glob with / separators", option, entry)
			}
		}
		if seen[pattern] {
			return nil, fmt.Errorf("duplicate %s pattern %q", option, entry)
		}
		seen[pattern] = true
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

func getUsage() string {
	return "Usage of cdnware:\n\n$ cdnware [OPTIONS] [SITEROOT]\n"
}

func loadSettings(args []string) (Settings, error) {
	defaults, err := loadDefaultConfig()
	if err != nil {
		return Settings{}, err
	}
	fs := &flagSet{fs: flag.NewFlagSet("cdnware", flag.ContinueOnError)}
	fs.fs.StringVar(&fs.cdn, "cdn", *defaults.Cdn, "CDN base URL")
	fs.fs.StringVar(&fs.src, "src", *defaults.Src, "source directory for assets, relative to SITEROOT")
	fs.fs.StringVar(&fs.dest, "dest", *defaults.Dest, "destination directory for revisioned assets, relative to SITEROOT")
	fs.fs.Var(&fs.revInclude, "rev-include", "glob of source-relative paths to revise (repeatable, e.g. **/*.css); empty revises nothing")
	fs.fs.Var(&fs.revExclude, "rev-exclude", "glob of source-relative paths not to revise (repeatable); empty excludes nothing")
	fs.fs.StringVar(&fs.configPath, "config", "", `Path to config file (.toml/.yaml/.yml/.json). Use "-" to disable auto-discovery.`)
	fs.fs.Usage = func() {
		fmt.Println(getUsage())
		fmt.Println("Options:")
		fs.fs.PrintDefaults()
	}
	if err := fs.fs.Parse(args); err != nil {
		return Settings{}, err
	}

	baseDir := "."
	if fs.fs.NArg() > 0 {
		baseDir = fs.fs.Arg(0)
	}
	explicit := map[string]bool{}
	fs.fs.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})

	var fileCfg *Config
	switch fs.configPath {
	case "":
		if path := discoverConfig(baseDir); path != "" {
			fileCfg, err = loadConfigFile(path)
		}
	case "-":
	default:
		fileCfg, err = loadConfigFile(fs.configPath)
	}
	if err != nil {
		return Settings{}, err
	}
	return mergeSettings(baseDir, defaults, fileCfg, fs, explicit)
}

func main() {
	settings, err := loadSettings(os.Args[1:])
	check(err)
	revisioner, err := newRevisioner(settings.BaseDir, settings.Cdn, settings.Src, settings.Dest, settings.RevInclude, settings.RevExclude)
	check(err)
	manifest, err := revisioner.revise()
	check(err)
	check(useman(manifest, settings.BaseDir, settings.Src, settings.Dest))
	jsonData, err := json.MarshalIndent(manifest, "", "  ")
	check(err)
	fmt.Printf("%s\n", jsonData)
}
