package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Namespace contains local preparation hints, never deployment authorization.
// A non-nil namespace selects shared-project behavior, without a feature flag.
type Namespace struct {
	CDNBaseURL  string
	AssetPrefix string
}

type projectTarget struct {
	URL         *string   `toml:"url"`
	Site        *string   `toml:"site"`
	Branch      *string   `toml:"branch"`
	Directory   *string   `toml:"directory"`
	CDNPrefixes *[]string `toml:"cdn_prefixes"`
	APIKeyEnv   *string   `toml:"api_key_env"`
	CDNBaseURL  *string   `toml:"cdn_base_url"`
	AssetPrefix *string   `toml:"asset_prefix"`
}

type sharedProject struct {
	Takeoff map[string]projectTarget  `toml:"takeoff"`
	Cdnware *Config                   `toml:"cdnware"`
	Tool    map[string]toml.Primitive `toml:"tool"`
}

// loadProject reads only local TOML. In particular, api_key_env is data, not an
// instruction to read an environment variable or a credential file.
func loadProject(path, target string) (*Config, string, *Namespace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", nil, fmt.Errorf("cannot read project file %s", path)
	}
	var project sharedProject
	metadata, err := toml.Decode(string(data), &project)
	if err != nil {
		// Decoder errors can include unrelated secret values from the project.
		return nil, "", nil, errors.New("invalid project.toml: expected supported fields in [takeoff.<environment>] and [cdnware] tables")
	}
	for _, key := range metadata.Undecoded() {
		if key[0] == "takeoff" || key[0] == "cdnware" {
			return nil, "", nil, errors.New("invalid project.toml: unknown takeoff or cdnware setting")
		}
	}
	if _, obsolete := project.Tool["takeoff"]; obsolete {
		return nil, "", nil, errors.New("obsolete [tool.takeoff] configuration; use [takeoff.default]")
	}
	if project.Cdnware == nil {
		return nil, "", nil, nil
	}
	if project.Cdnware.Cdn != nil {
		return nil, "", nil, errors.New("shared [cdnware] does not accept cdn; configure cdn_base_url and asset_prefix on each takeoff target")
	}
	// Validate shared directories before flag merging, just as Takeoff does.
	for _, dir := range []*string{project.Cdnware.Src, project.Cdnware.Dest} {
		if dir != nil {
			*dir, err = normalizeAssetDirectory(*dir)
			if err != nil {
				return nil, "", nil, err
			}
		}
	}
	selected, ok := project.Takeoff[target]
	if !ok {
		return nil, "", nil, fmt.Errorf("unknown takeoff environment %q; configure its [takeoff.<environment>] table", target)
	}
	if selected.CDNBaseURL == nil || selected.AssetPrefix == nil {
		return nil, "", nil, fmt.Errorf("takeoff environment %q requires cdn_base_url and asset_prefix for preparation", target)
	}
	namespace := &Namespace{CDNBaseURL: *selected.CDNBaseURL, AssetPrefix: *selected.AssetPrefix}
	if err := validateNamespace(namespace); err != nil {
		return nil, "", nil, err
	}
	directory := "./_site"
	if selected.Directory != nil {
		directory = *selected.Directory
	}
	if directory == "" {
		return nil, "", nil, errors.New("source directory must not be empty")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(filepath.Dir(path), directory)
	}
	return project.Cdnware, filepath.Clean(directory), namespace, nil
}

func normalizeAssetDirectory(value string) (string, error) {
	invalid := errors.New("expected a nonempty relative asset directory inside the selected output")
	if filepath.IsAbs(value) || strings.ContainsAny(value, "\\\x00") || hasDrivePrefix(value) {
		return "", invalid
	}
	parts := []string{}
	for _, part := range strings.Split(value, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", invalid
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", invalid
	}
	normalized := strings.Join(parts, "/")
	if hasDrivePrefix(normalized) {
		return "", invalid
	}
	return normalized, nil
}

func hasDrivePrefix(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func validateNamespace(namespace *Namespace) error {
	u, err := url.Parse(namespace.CDNBaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(namespace.CDNBaseURL, " \t\r\n\\?#") {
		return errors.New("cdn_base_url must be an HTTPS origin without credentials, path, query, or fragment")
	}
	namespace.CDNBaseURL = strings.TrimSuffix(namespace.CDNBaseURL, "/")
	prefix := namespace.AssetPrefix
	if !strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "\\\x00?#%") || hasDrivePrefix(prefix) {
		return errors.New("asset_prefix must be a relative, unescaped path ending in /")
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\r\n\t") {
			return errors.New("asset_prefix must be a relative, unescaped path ending in /")
		}
	}
	return nil
}

func resolveSettings(baseDir string, defaults *Config, fs *flagSet, explicit map[string]bool) (Settings, error) {
	if explicit["project"] && explicit["config"] {
		return Settings{}, errors.New("--project and --config select different configuration sources; use only one")
	}
	var fileCfg *Config
	var namespace *Namespace
	var err error
	if !explicit["config"] {
		projectPath := fs.projectPath
		if projectPath == "" {
			// Match Takeoff: CWD/project.toml only, never walk ancestors or SITEROOT.
			projectPath = "project.toml"
		}
		projectPath, err = filepath.Abs(projectPath)
		if err != nil {
			return Settings{}, err
		}
		_, statErr := os.Stat(projectPath)
		if statErr == nil || explicit["project"] || !os.IsNotExist(statErr) {
			var projectDir string
			fileCfg, projectDir, namespace, err = loadProject(projectPath, fs.target)
			if err != nil {
				return Settings{}, err
			}
			if namespace != nil && fs.fs.NArg() == 0 {
				baseDir = projectDir
			}
		}
		if namespace == nil && (explicit["project"] || explicit["target"]) {
			return Settings{}, errors.New("shared preparation requires [cdnware] and the selected [takeoff.<environment>] in project.toml")
		}
	}
	if namespace == nil {
		if explicit["target"] {
			return Settings{}, errors.New("--target requires a shared project, not a standalone config")
		}
		path := fs.configPath
		if path == "" {
			path = discoverConfig(baseDir)
		}
		if path != "-" {
			fileCfg, err = loadConfigFile(path)
		}
		if err != nil {
			return Settings{}, err
		}
	}
	settings, err := mergeSettings(baseDir, defaults, fileCfg, fs, explicit)
	if err != nil || namespace == nil {
		return settings, err
	}
	if explicit["cdn"] {
		namespace.CDNBaseURL = fs.cdn
		if err := validateNamespace(namespace); err != nil {
			return Settings{}, err
		}
	}
	settings.Cdn = namespace.CDNBaseURL
	settings.Namespace = namespace
	settings.Src, err = normalizeAssetDirectory(settings.Src)
	if err == nil {
		settings.Dest, err = normalizeAssetDirectory(settings.Dest)
	}
	return settings, err
}
