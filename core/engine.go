//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ShepFX/jellyfin-rpc/internal/discord"
)

const (
	pollEvery   = 2 * time.Second
	jellyfinPNG = "https://raw.githubusercontent.com/jellyfin/jellyfin-ux/master/branding/ios/icon.png"
	maxURLLen   = 256 // Discord rejects longer image URLs
)

type engine struct {
	dir  string
	cfg  *Config
	jf   *jfClient
	dc   *discord.Client
	http *http.Client

	art        map[string]string // item id -> public image URL ("" = none)
	series     map[string]map[string]string
	shown      *discord.Activity
	pausedAt   time.Time
	lastItem   string
	lastLogErr string
	nextSignIn time.Time
}

func newEngine(dir string) *engine {
	cfg := loadConfig(dir)
	_ = cfg.save(dir) // persist the generated device id
	hc := &http.Client{Timeout: 15 * time.Second}
	return &engine{
		dir:    dir,
		cfg:    cfg,
		dc:     discord.New(cfg.DiscordClientID),
		http:   hc,
		jf:     &jfClient{deviceID: cfg.DeviceID, http: hc},
		art:    map[string]string{},
		series: map[string]map[string]string{},
	}
}

func (e *engine) run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("engine crashed: %v", r) // never take Jellyfin Desktop down with us
		}
		_ = e.dc.SetActivity(nil)
		e.dc.Close()
	}()
	for ctx.Err() == nil {
		if !e.connect(ctx) {
			sleep(ctx, 15*time.Second)
			continue
		}
		e.tick(ctx)
		sleep(ctx, pollEvery)
	}
}

// connect makes sure we know the server and hold a working token.
func (e *engine) connect(ctx context.Context) bool {
	server := normaliseServer(e.cfg.Server)
	if server == "" {
		server = desktopServer()
	}
	if server == "" {
		e.logOnce("no server yet: sign in to a server in Jellyfin Desktop (or set \"server\" in config.json)")
		return false
	}
	if server != e.jf.server {
		log.Printf("server: %s", server)
		e.jf.server = server
		e.art, e.series = map[string]string{}, map[string]map[string]string{}
	}
	if e.jf.token == "" {
		e.jf.token = e.cfg.accessToken()
	}
	if e.jf.token != "" && e.cfg.UserID != "" {
		return true
	}
	return e.signIn(ctx)
}

// signIn runs a Quick Connect sign-in: show a code, wait for the user to
// approve it in Jellyfin, keep the token (encrypted) for next time.
func (e *engine) signIn(ctx context.Context) bool {
	e.jf.token = ""
	if time.Now().Before(e.nextSignIn) {
		return false // don't pop up a new code every few seconds after a missed one
	}
	e.nextSignIn = time.Now().Add(30 * time.Minute)
	if !e.jf.quickConnectEnabled(ctx) {
		e.logOnce("Quick Connect is off on this server: an admin can enable it in Dashboard > General")
		alertOnce(e, "Jellyfin RPC", "Jellyfin RPC signs in with Quick Connect, which is turned off on this server.\n\nAsk the server admin to enable it (Dashboard > General > Quick Connect), then restart Jellyfin Desktop.")
		return false
	}
	qc, err := e.jf.quickConnectStart(ctx)
	if err != nil {
		e.logOnce("quick connect: " + err.Error())
		return false
	}
	log.Printf("sign-in: Quick Connect code %s (approve it in Jellyfin > Settings > Quick Connect)", qc.Code)
	go alert("Jellyfin RPC - sign in", fmt.Sprintf(
		"To show what you watch on Discord, approve this code in Jellyfin:\n\n        %s\n\nIn Jellyfin Desktop: your profile picture > Settings > Quick Connect, enter the code and press Authorize.\n\nYou only need to do this once.", qc.Code))
	wctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	a, err := e.jf.quickConnectWait(wctx, qc.Secret)
	if err != nil || a.AccessToken == "" {
		log.Printf("sign-in not completed: %v", err)
		return false
	}
	e.jf.token = a.AccessToken
	e.cfg.setAccessToken(a.AccessToken)
	e.cfg.UserID, e.cfg.UserName = a.User.ID, a.User.Name
	if err := e.cfg.save(e.dir); err != nil {
		log.Printf("saving config: %v", err)
	}
	log.Printf("signed in as %s", a.User.Name)
	return true
}

func (e *engine) tick(ctx context.Context) {
	all, err := e.jf.sessions(ctx)
	if errors.Is(err, errUnauthorized) {
		log.Printf("token rejected (signed out or revoked): signing in again")
		e.cfg.Token, e.cfg.UserID, e.jf.token = "", "", ""
		_ = e.cfg.save(e.dir)
		e.show(nil)
		return
	}
	if err != nil {
		e.logOnce("sessions: " + err.Error())
		return // keep whatever is shown; the server may be restarting
	}
	s := e.pick(all)
	if s == nil {
		e.lastItem = ""
		e.show(nil)
		return
	}
	it := s.NowPlayingItem
	if it.ID != e.lastItem {
		e.lastItem = it.ID
		e.pausedAt = time.Time{}
	}
	paused := s.PlayState.IsPaused
	if paused {
		if e.pausedAt.IsZero() {
			e.pausedAt = time.Now()
		}
		if e.cfg.HidePausedAfter > 0 && time.Since(e.pausedAt) > time.Duration(e.cfg.HidePausedAfter)*time.Minute {
			e.show(nil)
			return
		}
	} else {
		e.pausedAt = time.Time{}
	}
	e.show(e.build(ctx, s, paused))
}

// pick finds this PC's playing session for the signed-in user: Jellyfin
// Desktop on this machine first, then any Jellyfin Desktop session of ours.
func (e *engine) pick(all []jfSession) *jfSession {
	host, _ := os.Hostname()
	var desktop []*jfSession
	for i := range all {
		s := &all[i]
		if s.NowPlayingItem == nil || !strings.EqualFold(s.UserID, e.cfg.UserID) {
			continue
		}
		if !strings.Contains(strings.ToLower(s.Client), "jellyfin desktop") && !strings.Contains(strings.ToLower(s.Client), "jellyfin media player") {
			continue
		}
		if strings.EqualFold(s.DeviceName, host) {
			return s
		}
		desktop = append(desktop, s)
	}
	if len(desktop) == 1 {
		return desktop[0]
	}
	return nil
}

func (e *engine) build(ctx context.Context, s *jfSession, paused bool) *discord.Activity {
	it := s.NowPlayingItem
	a := &discord.Activity{Type: discord.TypeWatching, StatusDisplayType: discord.DisplayDetails}
	var ids map[string]string
	artID := it.ID
	switch it.Type {
	case "Episode":
		if !on(e.cfg.ShowEpisodes) {
			return nil
		}
		a.Details = orDefault(it.SeriesName, it.Name)
		ep := ""
		if it.ParentIndexNumber != nil && it.IndexNumber != nil {
			ep = fmt.Sprintf("S%dE%d", *it.ParentIndexNumber, *it.IndexNumber)
		}
		a.State = join(" · ", ep, it.Name)
		if it.SeriesID != "" {
			artID = it.SeriesID
			ids = e.seriesIDs(ctx, it.SeriesID)
		}
	case "Audio":
		if !on(e.cfg.ShowMusic) {
			return nil
		}
		a.Type = discord.TypeListening
		a.Details = it.Name
		a.State = orDefault(strings.Join(it.Artists, ", "), it.AlbumArtist)
	default: // Movie, MusicVideo, Video ...
		if !on(e.cfg.ShowMovies) {
			return nil
		}
		a.Details = it.Name
		if it.ProductionYear > 0 {
			a.State = fmt.Sprint(it.ProductionYear)
		}
		ids = it.ProviderIDs
	}
	a.Details, a.State = clip(a.Details), clip(a.State)
	if len(a.State) < 2 {
		a.State = "" // Discord needs 2+ characters
	}

	img := e.image(ctx, it, artID)
	a.Assets = &discord.Assets{LargeImage: orDefault(img, jellyfinPNG), LargeText: clip(orDefault(it.Album, a.Details))}
	if img != "" {
		a.Assets.SmallImage, a.Assets.SmallText = jellyfinPNG, "Jellyfin"
	}
	if paused {
		// Discord's Watching/Listening card always draws a timer and shows
		// 00:00 without timestamps, and it can't freeze a progress bar. While
		// paused use the plain card (no timer) and say where playback stopped.
		a.Type, a.StatusDisplayType = discord.TypePlaying, discord.DisplayName
		a.Assets.SmallText = "Paused"
		at := "Paused"
		if it.RunTimeTicks > 0 {
			at = fmt.Sprintf("Paused at %s / %s", clock(s.PlayState.PositionTicks/10000), clock(it.RunTimeTicks/10000))
		}
		a.State = clip(join(" · ", a.State, at))
	} else if it.RunTimeTicks > 0 {
		pos := s.PlayState.PositionTicks / 10000
		start := time.Now().UnixMilli() - pos
		a.Timestamps = &discord.Timestamps{Start: start, End: start + it.RunTimeTicks/10000}
	}
	if on(e.cfg.ShowButtons) && ids != nil {
		if v := ids["Imdb"]; v != "" {
			a.Buttons = append(a.Buttons, discord.Button{Label: "IMDb", URL: "https://www.imdb.com/title/" + v})
		}
		if v := ids["Tmdb"]; v != "" {
			kind := "movie"
			if it.Type == "Episode" {
				kind = "tv"
			}
			a.Buttons = append(a.Buttons, discord.Button{Label: "TMDB", URL: "https://www.themoviedb.org/" + kind + "/" + v})
		}
	}
	return a
}

func (e *engine) seriesIDs(ctx context.Context, id string) map[string]string {
	if v, ok := e.series[id]; ok {
		return v
	}
	v, err := e.jf.providerIDs(ctx, e.cfg.UserID, id)
	if err != nil {
		return nil // retry next tick
	}
	e.series[id] = v
	return v
}

// image finds a public poster URL that never points at the Jellyfin server:
// the metadata provider's poster (TMDB) for films and shows, iTunes cover art
// for music. "" = none found, use the Jellyfin logo.
func (e *engine) image(ctx context.Context, it *jfItem, artID string) string {
	key := artID
	if it.Type == "Audio" {
		key = "album:" + orDefault(it.AlbumID, it.ID)
	}
	if v, ok := e.art[key]; ok {
		return v
	}
	var u string
	if it.Type == "Audio" {
		u = e.itunes(ctx, orDefault(it.AlbumArtist, strings.Join(it.Artists, " ")), orDefault(it.Album, it.Name))
	} else {
		u, _ = e.jf.remotePoster(ctx, artID)
	}
	if len(u) > maxURLLen {
		u = ""
	}
	e.art[key] = u
	return u
}

func (e *engine) itunes(ctx context.Context, artist, album string) string {
	q := url.Values{"term": {strings.TrimSpace(artist + " " + album)}, "entity": {"album"}, "limit": {"1"}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://itunes.apple.com/search?"+q.Encode(), nil)
	resp, err := e.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var r struct {
		Results []struct {
			Art string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || len(r.Results) == 0 {
		return ""
	}
	return strings.Replace(r.Results[0].Art, "100x100bb", "512x512bb", 1)
}

// show sends the activity to Discord only when it changed (Discord rate
// limits presence updates).
func (e *engine) show(a *discord.Activity) {
	if same(e.shown, a) {
		return
	}
	desc := "cleared"
	if a != nil {
		img := ""
		if a.Assets != nil {
			img = a.Assets.LargeImage
		}
		desc = fmt.Sprintf("%s | %s | %s | %d buttons", a.Details, a.State, img, len(a.Buttons))
	}
	if err := e.dc.SetActivity(a); err != nil {
		e.logOnce("discord: " + err.Error() + " (is the Discord desktop app running?) - wanted: " + desc)
		e.shown = nil
		return
	}
	log.Printf("presence: %s", desc)
	e.shown = a
}

// same treats activities as equal when only the progress bar drifted a little.
func same(x, y *discord.Activity) bool {
	if x == nil || y == nil {
		return x == y
	}
	if x.Details != y.Details || x.State != y.State || x.Type != y.Type || (x.Timestamps == nil) != (y.Timestamps == nil) {
		return false
	}
	if x.Assets != nil && y.Assets != nil && *x.Assets != *y.Assets {
		return false
	}
	if x.Timestamps != nil && abs(x.Timestamps.Start-y.Timestamps.Start) > 3000 {
		return false // seeked
	}
	return true
}

func (e *engine) logOnce(msg string) {
	if msg != e.lastLogErr {
		log.Print(msg)
		e.lastLogErr = msg
	}
}

var alerted = map[string]bool{}

func alertOnce(_ *engine, title, text string) {
	if !alerted[text] {
		alerted[text] = true
		go alert(title, text)
	}
}

func alert(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func join(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// clip keeps text within Discord's 128-character field limit.
func clip(s string) string {
	r := []rune(s)
	if len(r) > 128 {
		return string(r[:127]) + "…"
	}
	return s
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// clock formats milliseconds as 12:34 or 1:02:03.
func clock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
