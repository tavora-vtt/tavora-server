package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
)

func samplePNG(t *testing.T, width, height int, tint uint8) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: tint, A: 255})
		}
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buffer.Bytes()
}

// noisyPNG produces content png cannot compress, so a handful of uploads is enough to
// reach a quota that gradients would never fill.
func noisyPNG(t *testing.T, width, height int, seed uint32) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	state := seed*2654435761 + 1013904223
	for y := range height {
		for x := range width {
			state = state*1664525 + 1013904223
			canvas.Set(x, y, color.RGBA{
				R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 255,
			})
		}
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buffer.Bytes()
}

func (h *authHarness) uploadAs(t *testing.T, client *http.Client, worldID, filename string, content []byte) *http.Response {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}

	response, err := client.Post(
		h.server.URL+"/api/worlds/"+worldID+"/assets", form.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return response
}

func (h *authHarness) upload(t *testing.T, worldID, filename string, content []byte) *http.Response {
	t.Helper()
	return h.uploadAs(t, h.client, worldID, filename, content)
}

func (h *authHarness) uploadedAsset(t *testing.T, worldID string, content []byte) assetView {
	t.Helper()

	response := h.upload(t, worldID, "map.png", content)
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload returned %d", response.StatusCode)
	}

	var view assetView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	return view
}

func (h *authHarness) joinAsPlayer(t *testing.T, worldID string) *http.Client {
	t.Helper()

	invite := h.createInvite(t, worldID, `{"role":"player"}`)
	player := h.anonymous(t)

	accepted := h.postAs(t, player, "/api/invites/"+invite.Token+"/accept", `{"username":"tomas"}`)
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusOK && accepted.StatusCode != http.StatusCreated {
		t.Fatalf("accepting the invite returned %d", accepted.StatusCode)
	}
	return player
}

func TestUploadingAnImageStoresItsDimensions(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	view := h.uploadedAsset(t, world.ID, samplePNG(t, 120, 80, 10))

	if view.Width != 120 || view.Height != 80 {
		t.Errorf("asset is %dx%d, want 120x80", view.Width, view.Height)
	}
	if view.Mime != "image/png" {
		t.Errorf("mime is %q", view.Mime)
	}
	if view.URL != "/assets/"+world.ID+"/"+view.ID {
		t.Errorf("url is %q", view.URL)
	}
	if view.Thumbnail == "" {
		t.Error("no thumbnail was produced")
	}
}

func TestTheSameImageUploadedTwiceIsStoredOnce(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	content := samplePNG(t, 64, 64, 20)

	first := h.uploadedAsset(t, world.ID, content)

	again := h.upload(t, world.ID, "copy-of-map.png", content)
	defer again.Body.Close()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("re-uploading returned %d, want 200", again.StatusCode)
	}

	var second assetView
	if err := json.NewDecoder(again.Body).Decode(&second); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("the same content produced two assets: %s and %s", first.ID, second.ID)
	}

	listed := h.get(t, "/api/worlds/"+world.ID+"/assets")
	defer listed.Body.Close()

	var views []assetView
	if err := json.NewDecoder(listed.Body).Decode(&views); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(views) != 1 {
		t.Errorf("the world lists %d assets, want 1", len(views))
	}
}

func TestAPlayerCannotUploadButCanRead(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	view := h.uploadedAsset(t, world.ID, samplePNG(t, 48, 48, 30))
	player := h.joinAsPlayer(t, world.ID)

	refused := h.uploadAs(t, player, world.ID, "sneaky.png", samplePNG(t, 16, 16, 40))
	defer refused.Body.Close()
	if refused.StatusCode != http.StatusForbidden {
		t.Errorf("a player uploaded an asset: %d", refused.StatusCode)
	}

	fetched, err := player.Get(h.server.URL + view.URL)
	if err != nil {
		t.Fatalf("fetch asset: %v", err)
	}
	defer fetched.Body.Close()
	if fetched.StatusCode != http.StatusOK {
		t.Fatalf("a player could not read the map: %d", fetched.StatusCode)
	}
}

func TestAnOutsiderCannotReadAWorldsAssets(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	view := h.uploadedAsset(t, world.ID, samplePNG(t, 32, 32, 50))

	response, err := h.anonymous(t).Get(h.server.URL + view.URL)
	if err != nil {
		t.Fatalf("fetch asset: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK {
		t.Error("an anonymous request read a world's map")
	}
}

func TestServedAssetsCannotBeInterpretedAsDocuments(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	view := h.uploadedAsset(t, world.ID, samplePNG(t, 64, 32, 60))

	response := h.get(t, view.URL)
	defer response.Body.Close()

	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options is %q", got)
	}
	if got := response.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("assets are served without a content security policy")
	}
	if got := response.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type is %q, want image/png", got)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("what came back is not the stored image: %v", err)
	}
}

func TestTheThumbnailVariantIsSmallerThanTheImage(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	view := h.uploadedAsset(t, world.ID, samplePNG(t, 900, 600, 70))

	response := h.get(t, view.Thumbnail)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fetching the thumbnail returned %d", response.StatusCode)
	}

	decoded, err := png.Decode(response.Body)
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if decoded.Bounds().Dx() > 256 || decoded.Bounds().Dy() > 256 {
		t.Errorf("the thumbnail is %v, which is not a thumbnail", decoded.Bounds())
	}
}

func TestUploadsThatAreNotImagesAreRefused(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	response := h.upload(t, world.ID, "portrait.png", svg)
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("an svg named .png returned %d, want 415", response.StatusCode)
	}
}

func TestAWorldCannotBeFilledPastItsQuota(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)

	refused := false
	for attempt := range uint32(8) {
		response := h.upload(t, world.ID, "map.png", noisyPNG(t, 700, 700, attempt+1))
		status := response.StatusCode
		response.Body.Close()

		if status == http.StatusRequestEntityTooLarge {
			refused = true
			break
		}
		if status != http.StatusCreated {
			t.Fatalf("upload %d returned %d", attempt, status)
		}
	}

	if !refused {
		t.Fatalf("a world absorbed more than its %d byte quota", testWorldQuota)
	}
}
