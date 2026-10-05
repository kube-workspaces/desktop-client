// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

type avatarState struct {
	key        string
	generation uint64
	cancel     context.CancelFunc
	image      image.Image
	raster     *image.NRGBA
}

func (a *App) avatarKey() string {
	who := a.m.Identity
	if who == nil || who.AvatarURL == "" || a.m.State == StateLogin || a.m.State == StateServer {
		return ""
	}
	return a.m.Server + "\n" + who.Email + "\n" + who.AvatarURL
}

func (a *App) stopAvatar() {
	if a.avatar.cancel != nil {
		a.avatar.cancel()
	}
	a.avatar = avatarState{generation: a.avatar.generation + 1}
}

// Identity comes from /auth/me for both restored credentials and new logins.
// Never let a completed download from an old profile replace the current one.
func (a *App) reconcileAvatar(ctx context.Context) {
	key := a.avatarKey()
	if key == a.avatar.key {
		return
	}
	a.stopAvatar()
	a.dirty = true
	if key == "" {
		return
	}
	a.avatar.key = key
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	a.avatar.cancel = cancel
	generation, rawURL := a.avatar.generation, a.m.Identity.AvatarURL
	a.background(func() func() {
		defer cancel()
		img, err := fetchAvatar(opCtx, rawURL)
		return func() {
			if generation != a.avatar.generation || key != a.avatarKey() {
				return
			}
			a.avatar.cancel = nil
			if err == nil {
				a.avatar.image = img
			}
		}
	})
}

// Like the frontend's no-referrer image, this request carries no platform
// credential or Referer, including when the image host redirects it.
func fetchAvatar(ctx context.Context, rawURL string) (image.Image, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("invalid avatar URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.User != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
			return fmt.Errorf("invalid avatar redirect")
		}
		req.Header.Del("Referer")
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avatar HTTP %d", resp.StatusCode)
	}
	const maxBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("avatar exceeds size limit")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 4<<20 {
		return nil, fmt.Errorf("avatar exceeds pixel limit")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func avatarInitials(who *kwclient.Identity) string {
	if who == nil {
		return ""
	}
	var initials []rune
	for _, name := range strings.Fields(who.DisplayName) {
		for _, r := range name {
			initials = append(initials, unicode.ToUpper(r))
			break
		}
		if len(initials) == 2 {
			break
		}
	}
	if len(initials) == 0 {
		for _, r := range who.Email {
			initials = append(initials, unicode.ToUpper(r))
			break
		}
	}
	return string(initials)
}

// Cache the scaled, centre-cropped circle; a theme/DPI change rebuilds it.
func (a *App) drawAvatarImage(r ui.Rect) bool {
	if a.avatar.image == nil || a.avatar.key != a.avatarKey() || r.Empty() {
		return false
	}
	if a.avatar.raster == nil || a.avatar.raster.Bounds().Dx() != r.W || a.avatar.raster.Bounds().Dy() != r.H {
		img := image.NewNRGBA(image.Rect(0, 0, r.W, r.H))
		src := a.avatar.image.Bounds()
		side := min(src.Dx(), src.Dy())
		src = image.Rect(src.Min.X+(src.Dx()-side)/2, src.Min.Y+(src.Dy()-side)/2, src.Min.X+(src.Dx()+side)/2, src.Min.Y+(src.Dy()+side)/2)
		draw.CatmullRom.Scale(img, img.Bounds(), a.avatar.image, src, draw.Src, nil)
		rad := float64(min(r.W, r.H)) / 2
		for y := 0; y < r.H; y++ {
			for x := 0; x < r.W; x++ {
				coverage := max(0, min(1, rad+0.5-math.Hypot(float64(x)+0.5-float64(r.W)/2, float64(y)+0.5-float64(r.H)/2)))
				i := img.PixOffset(x, y) + 3
				img.Pix[i] = uint8(math.Round(float64(img.Pix[i]) * coverage))
			}
		}
		a.avatar.raster = img
	}
	img := a.avatar.raster
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			c := img.NRGBAAt(x, y)
			a.ctx.Canvas.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: 1, H: 1}, color.RGBA(c))
		}
	}
	return true
}
