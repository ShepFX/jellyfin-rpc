//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var errUnauthorized = errors.New("jellyfin: token rejected")

type jfClient struct {
	server   string
	token    string
	deviceID string
	http     *http.Client
}

func (j *jfClient) authHeader() string {
	host, _ := os.Hostname()
	h := fmt.Sprintf(`MediaBrowser Client="Jellyfin RPC", Device=%q, DeviceId=%q, Version=%q`, host, j.deviceID, version)
	if j.token != "" {
		h += fmt.Sprintf(`, Token=%q`, j.token)
	}
	return h
}

func (j *jfClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.server+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", j.authHeader())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := j.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("jellyfin %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Quick Connect sign-in ---------------------------------------------------

type qcResult struct {
	Secret        string `json:"Secret"`
	Code          string `json:"Code"`
	Authenticated bool   `json:"Authenticated"`
}

type authResult struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

func (j *jfClient) quickConnectEnabled(ctx context.Context) bool {
	var on bool
	return j.do(ctx, "GET", "/QuickConnect/Enabled", nil, &on) == nil && on
}

func (j *jfClient) quickConnectStart(ctx context.Context) (*qcResult, error) {
	var r qcResult
	return &r, j.do(ctx, "POST", "/QuickConnect/Initiate", nil, &r)
}

// quickConnectWait polls until the code is approved (or ctx ends), then
// exchanges it for an access token.
func (j *jfClient) quickConnectWait(ctx context.Context, secret string) (*authResult, error) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		var st qcResult
		if err := j.do(ctx, "GET", "/QuickConnect/Connect?secret="+url.QueryEscape(secret), nil, &st); err != nil {
			return nil, err // expired (codes last a few minutes) or server gone
		}
		if !st.Authenticated {
			continue
		}
		var a authResult
		err := j.do(ctx, "POST", "/Users/AuthenticateWithQuickConnect", map[string]string{"Secret": secret}, &a)
		return &a, err
	}
}

// --- sessions -----------------------------------------------------------------

type jfItem struct {
	ID                    string            `json:"Id"`
	Name                  string            `json:"Name"`
	Type                  string            `json:"Type"`
	SeriesName            string            `json:"SeriesName"`
	SeriesID              string            `json:"SeriesId"`
	ParentIndexNumber     *int              `json:"ParentIndexNumber"`
	IndexNumber           *int              `json:"IndexNumber"`
	ProductionYear        int               `json:"ProductionYear"`
	RunTimeTicks          int64             `json:"RunTimeTicks"`
	ProviderIDs           map[string]string `json:"ProviderIds"`
	Album                 string            `json:"Album"`
	AlbumID               string            `json:"AlbumId"`
	AlbumArtist           string            `json:"AlbumArtist"`
	Artists               []string          `json:"Artists"`
}

type jfSession struct {
	UserID         string  `json:"UserId"`
	Client         string  `json:"Client"`
	DeviceName     string  `json:"DeviceName"`
	DeviceID       string  `json:"DeviceId"`
	LastActivity   string  `json:"LastActivityDate"`
	NowPlayingItem *jfItem `json:"NowPlayingItem"`
	PlayState      struct {
		PositionTicks int64 `json:"PositionTicks"`
		IsPaused      bool  `json:"IsPaused"`
	} `json:"PlayState"`
}

func (j *jfClient) sessions(ctx context.Context) ([]jfSession, error) {
	var s []jfSession
	return s, j.do(ctx, "GET", "/Sessions?activeWithinSeconds=120", nil, &s)
}

// providerIDs fetches an item's provider IDs (used for the series of an episode).
func (j *jfClient) providerIDs(ctx context.Context, userID, itemID string) (map[string]string, error) {
	var it struct {
		ProviderIDs map[string]string `json:"ProviderIds"`
	}
	err := j.do(ctx, "GET", "/Users/"+userID+"/Items/"+itemID, nil, &it)
	return it.ProviderIDs, err
}

// remotePoster asks Jellyfin which public poster its metadata providers
// (TheMovieDb etc.) have for an item. The URL points at the provider, never at
// the Jellyfin server.
func (j *jfClient) remotePoster(ctx context.Context, itemID string) (string, error) {
	var r struct {
		Images []struct {
			URL      string `json:"Url"`
			Language string `json:"Language"`
		} `json:"Images"`
	}
	if err := j.do(ctx, "GET", "/Items/"+itemID+"/RemoteImages?type=Primary&limit=10", nil, &r); err != nil {
		return "", err
	}
	best := ""
	for _, im := range r.Images {
		if !strings.HasPrefix(im.URL, "https://") {
			continue
		}
		if best == "" {
			best = im.URL
		}
		if im.Language == "en" || im.Language == "" {
			best = im.URL
			break
		}
	}
	// TMDB "original" posters are huge; Discord only needs a small one.
	return strings.Replace(best, "/t/p/original/", "/t/p/w500/", 1), nil
}
