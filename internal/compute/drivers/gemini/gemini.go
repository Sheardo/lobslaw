// Package gemini implements Google's generateContent wire shape.
//
// Vision only, for now. Gemini's chat path is the same endpoint family
// and would be a natural addition, but shipping the modality that
// already had an inline implementation is what this move is for —
// adding a chat driver at the same time would mix a refactor with a
// feature and leave neither reviewable.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// DriverName is what `driver = "gemini"` resolves to.
const DriverName = "gemini"

// VisionFactory is the compute.VisionDriverFactory for Gemini.
func VisionFactory(cfg compute.VisionDriverConfig) (compute.VisionDriver, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("gemini vision: endpoint required")
	}
	if cfg.Model == "" {
		return nil, errors.New("gemini vision: model required")
	}
	return &visionDriver{cfg: cfg, client: compute.HTTPClientOr(cfg.HTTPClient)}, nil
}

type visionDriver struct {
	cfg    compute.VisionDriverConfig
	client *http.Client
}

func (d *visionDriver) Describe(ctx context.Context, req compute.VisionRequest) (string, error) {
	body, err := json.Marshal(visionRequest{
		Contents: []content{{
			Parts: []part{
				{Text: req.Question},
				{InlineData: &inlineData{
					MIMEType: req.MIME,
					Data:     base64.StdEncoding.EncodeToString(req.Data),
				}},
			},
		}},
	})
	if err != nil {
		return "", err
	}
	raw, err := compute.DoVisionRequest(ctx, d.client, d.cfg, http.MethodPost, d.cfg.Endpoint, body)
	if err != nil {
		return "", err
	}
	return decodeVision(raw)
}

type visionRequest struct {
	Contents []content `json:"contents"`
}
type content struct {
	Parts []part `json:"parts"`
}
type part struct {
	Text       string      `json:"text,omitempty"`
	InlineData *inlineData `json:"inlineData,omitempty"`
}
type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

// decodeVision concatenates every text part of every candidate.
func decodeVision(raw []byte) (string, error) {
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, cand := range decoded.Candidates {
		for _, p := range cand.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}
