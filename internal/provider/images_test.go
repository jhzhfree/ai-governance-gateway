package provider_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestInlineImageValidationAndMetadata(t *testing.T) {
	for _, mode := range []string{"valid", "remote", "svg", "mime", "base64", "bytes", "dimensions", "count", "role", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			uri := testutil.InlinePNG
			switch mode {
			case "remote":
				uri = "http://127.0.0.1/private"
			case "svg":
				uri = "data:image/svg+xml;base64,PHN2Zz4="
			case "mime":
				uri = strings.Replace(uri, "image/png", "image/jpeg", 1)
			case "base64":
				uri += "!"
			case "bytes":
				uri = "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, (256<<10)+1))
			case "dimensions":
				var buf bytes.Buffer
				if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4097, 1))); err != nil {
					t.Fatal(err)
				}
				uri = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
			}
			req := provider.Request{Model: "zhipu/fixture", MaxTokens: 8, Messages: []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: uri}}}}}}
			if mode == "count" {
				for range 4 {
					req.Messages[0].Parts = append(req.Messages[0].Parts, req.Messages[0].Parts[0])
				}
			}
			if mode == "role" {
				req.Messages[0].Role = "system"
			}
			if mode == "unknown-field" {
				raw := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + uri + `","unsafe":true}}]}`
				var message provider.Message
				if json.Unmarshal([]byte(raw), &message) == nil {
					t.Fatal("nested unknown image field accepted")
				}
				return
			}
			if provider.ValidateRequest(req) != (mode == "valid") {
				t.Fatal("image admission boundary failed")
			}
			if mode == "valid" {
				raw, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				var roundtrip provider.Request
				if json.Unmarshal(raw, &roundtrip) != nil || !provider.ValidateRequest(roundtrip) {
					t.Fatal("image lost during round trip")
				}
				meta, err := roundtrip.ImageMetadata()
				if err != nil || len(meta) != 1 || meta[0].Width != 1 || meta[0].Height != 1 || len(meta[0].SHA256) != 64 || meta[0].MIME != "image/png" {
					t.Fatal("image evidence incorrect")
				}
			}
		})
	}
}
func TestImageApprovalChangesImmutableRouteIdentity(t *testing.T) {
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
	}
	plain, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Models {
		cfg.Models[i].AllowImages = true
	}
	images, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := plain.Find("zhipu/fixture")
	after, _ := images.Find("zhipu/fixture")
	if before.AllowsImages() || !after.AllowsImages() || before.PriceVersion() == after.PriceVersion() {
		t.Fatal("image approval absent from route snapshot")
	}
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 0
	}
	if _, err := provider.New(cfg, func(string) string { return "synthetic" }, nil); err == nil {
		t.Fatal("native executor accepted unimplemented image capability")
	}
}
