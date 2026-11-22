//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultClientID is the public "Jellyfin" Discord application (the one the
// jellyfin-rpc project uses), so the profile card reads "Watching Jellyfin".
const DefaultClientID = "1053747938519679018"

// Config lives in %APPDATA%\jellyfin-rpc\config.json. Everything is optional:
// the server comes from Jellyfin Desktop's own settings and the token from a
// one-time Quick Connect sign-in.
type Config struct {
	Server          string `json:"server,omitempty"`  // override; empty = read from Jellyfin Desktop
	Token           string `json:"token,omitempty"`   // DPAPI-encrypted access token (base64)
	UserID          string `json:"user_id,omitempty"` // set by sign-in
	UserName        string `json:"user_name,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	DiscordClientID string `json:"discord_client_id,omitempty"`
	ShowMovies      *bool  `json:"show_movies,omitempty"`
	ShowEpisodes    *bool  `json:"show_episodes,omitempty"`
	ShowMusic       *bool  `json:"show_music,omitempty"`
	ShowButtons     *bool  `json:"show_buttons,omitempty"`  // IMDb / TMDB links
	HidePausedAfter int    `json:"hide_paused_after_minutes"` // 0 = never hide while paused
}

func on(b *bool) bool { return b == nil || *b }

func loadConfig(dir string) *Config {
	c := &Config{HidePausedAfter: 5}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(b, c)
	}
	if c.DiscordClientID == "" {
		c.DiscordClientID = DefaultClientID
	}
	if c.DeviceID == "" {
		c.DeviceID = newDeviceID()
	}
	return c
}

func (c *Config) save(dir string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

func newDeviceID() string {
	host, _ := os.Hostname()
	return "jellyfin-rpc-" + strings.ToLower(host) + "-" + time.Now().Format("20060102150405")
}

// --- token encryption (Windows DPAPI, current user) ------------------------

func (c *Config) accessToken() string {
	if c.Token == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(c.Token)
	if err != nil {
		return ""
	}
	out, err := dpapi(raw, false)
	if err != nil {
		return ""
	}
	return string(out)
}

func (c *Config) setAccessToken(tok string) {
	enc, err := dpapi([]byte(tok), true)
	if err != nil {
		return
	}
	c.Token = base64.StdEncoding.EncodeToString(enc)
}

func dpapi(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// --- server address from Jellyfin Desktop -----------------------------------

// desktopServer reads the server Jellyfin Desktop connects to (its
// "userWebClient" setting) from the active profile, portable or installed.
func desktopServer() string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "data")) // portable mode
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		roots = append(roots, filepath.Join(la, "Jellyfin Desktop"), filepath.Join(la, "jellyfin-desktop"))
	}
	for _, root := range roots {
		var confs []string
		var prof struct {
			DefaultProfile string `json:"defaultProfile"`
		}
		if b, err := os.ReadFile(filepath.Join(root, "profiles.json")); err == nil && json.Unmarshal(b, &prof) == nil && prof.DefaultProfile != "" {
			confs = append(confs, filepath.Join(root, "profiles", prof.DefaultProfile, "jellyfin-desktop.conf"))
		}
		all, _ := filepath.Glob(filepath.Join(root, "profiles", "*", "jellyfin-desktop.conf"))
		sort.Slice(all, func(i, j int) bool { return modTime(all[i]).After(modTime(all[j])) })
		confs = append(confs, all...)
		confs = append(confs, filepath.Join(root, "jellyfin-desktop.conf"))
		for _, p := range confs {
			if s := readWebClient(p); s != "" {
				return s
			}
		}
	}
	return ""
}

func readWebClient(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var conf struct {
		Sections map[string]map[string]any `json:"sections"`
	}
	if json.Unmarshal(b, &conf) != nil {
		return ""
	}
	if s, ok := conf.Sections["main"]["userWebClient"].(string); ok {
		return normaliseServer(s)
	}
	return ""
}

// normaliseServer turns "https://host/web/index.html#/home" into "https://host".
func normaliseServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(strings.ToLower(s), "/web"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "/")
}

func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
