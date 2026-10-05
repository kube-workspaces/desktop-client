// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/ui"
)

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 2 && x < 6 {
				c = color.NRGBA{G: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFetchAvatarRedirectAndFailures(t *testing.T) {
	data := avatarPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "Cookie", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Errorf("avatar sent %s", name)
			}
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/image", http.StatusFound)
		case "/image":
			_, _ = w.Write(data)
		case "/bad":
			_, _ = w.Write([]byte("not an image"))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	img, err := fetchAvatar(context.Background(), srv.URL+"/redirect")
	if err != nil || img.Bounds().Dx() != 8 {
		t.Fatalf("redirected image = %v, %v", img, err)
	}
	for _, rawURL := range []string{srv.URL + "/bad", srv.URL + "/large", srv.URL + "/missing", "file:///tmp/avatar", "https://user:password@example.com/a"} {
		if _, err := fetchAvatar(context.Background(), rawURL); err == nil {
			t.Errorf("accepted invalid avatar %s", rawURL)
		}
	}
}

func TestAvatarProfileLifecycleAndRendering(t *testing.T) {
	data := avatarPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.m.Identity = &kwclient.Identity{Email: "one@example.com", AvatarURL: srv.URL}
	r.step()
	r.settle()
	a := r.app
	if a.avatar.image == nil {
		t.Fatal("signed-in identity did not load avatar")
	}
	box := ui.Rect{X: 10, Y: 10, W: 20, H: 20}
	a.ctx.Canvas.Fill(box, color.RGBA{B: 255, A: 255})
	if !a.drawAvatarImage(box) {
		t.Fatal("loaded avatar did not render")
	}
	if got := a.ctx.Canvas.Image().RGBAAt(20, 20); got.G != 255 || got.R != 0 {
		t.Fatalf("centre crop = %v, want green", got)
	}
	if got := a.ctx.Canvas.Image().RGBAAt(10, 10); got.B != 255 {
		t.Fatalf("circular corner = %v, want background", got)
	}
	a.m.Identity = &kwclient.Identity{Email: "two@example.com"}
	if a.drawAvatarImage(box) {
		t.Fatal("new profile rendered old user's avatar")
	}
	r.step()
	if a.avatar.image != nil {
		t.Fatal("profile without avatar retained old image")
	}
}

func TestAvatarDiscardLateDownload(t *testing.T) {
	data := avatarPNG(t)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	r := newRig(savedProfile(), "token")
	r.start()
	r.app.m.Identity = &kwclient.Identity{Email: "one@example.com", AvatarURL: srv.URL}
	r.step()
	<-started
	r.app.m.Identity = &kwclient.Identity{Email: "two@example.com"}
	close(release)
	r.settle()
	if r.app.avatar.image != nil {
		t.Fatal("late result restored previous user's avatar")
	}
}

func TestAvatarInitials(t *testing.T) {
	for _, tc := range []struct {
		who  *kwclient.Identity
		want string
	}{
		{nil, ""},
		{&kwclient.Identity{DisplayName: " Ada Lovelace "}, "AL"},
		{&kwclient.Identity{DisplayName: "Élodie 张"}, "É张"},
		{&kwclient.Identity{Email: "user@example.com"}, "U"},
	} {
		if got := avatarInitials(tc.who); got != tc.want {
			t.Errorf("initials = %q, want %q", got, tc.want)
		}
	}
}
