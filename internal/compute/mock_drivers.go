package compute

import (
	"context"
	"strings"
)

// The mock driver set.
//
// R22 asks that a node whose every provider is `driver = "mock"` boots
// with NO NETWORK ACCESS and serves a full turn. Chat, vision, audio,
// embeddings and jobs already had a mock; speech and image generation
// did not, so such a node booted and then failed the moment somebody
// asked it to say something out loud.
//
// This is not only a testing convenience. It is the cheapest possible
// check that the driver seam is complete: a modality with no mock is a
// modality whose wire shape has not actually been separated from its
// plumbing, because if it had been, substituting the wire would be
// trivial.
//
// Every mock produces PLAUSIBLE output rather than empty values. A
// zero-length artifact would exercise none of the delivery path it
// exists to let somebody exercise.

// mockSpeechWAV is a minimal but VALID RIFF/WAVE header followed by a
// few frames of silence.
//
// Valid rather than arbitrary bytes, because the artifact path sniffs
// and forwards this to a channel — Telegram rejects a voice note whose
// container it cannot parse, and a mock that produced garbage would
// make the delivery path untestable exactly where it is most fiddly.
var mockSpeechWAV = buildSilentWAV(1024)

// MockSpeakFactory synthesises silence.
func MockSpeakFactory(_ SpeakDriverConfig) (SpeakDriver, error) {
	return mockSpeakDriver{}, nil
}

type mockSpeakDriver struct{}

func (mockSpeakDriver) Speak(_ context.Context, req SpeakRequest) (*Artifact, error) {
	// The requested container is honoured in the MIME type even though
	// the bytes are always WAV: the caller's format handling is what a
	// mock is there to exercise, and returning audio/mpeg for a "wav"
	// request would send them chasing a bug that is not there.
	format := req.Format
	if format == "" {
		format = "wav"
	}
	return &Artifact{
		Kind:  ArtifactInline,
		Bytes: mockSpeechWAV,
		MIME:  mimeForAudioFormat(format),
	}, nil
}

// MockImageFactory returns a small valid PNG.
func MockImageFactory(_ ImageDriverConfig) (ImageDriver, error) {
	return mockImageDriver{}, nil
}

type mockImageDriver struct{}

func (mockImageDriver) Generate(_ context.Context, _ ImageRequest) (*Artifact, error) {
	return &Artifact{
		Kind:  ArtifactInline,
		Bytes: mockPNG(),
		MIME:  "image/png",
	}, nil
}

// buildSilentWAV writes a RIFF/WAVE header for n bytes of 8-bit mono
// PCM silence at 8kHz.
func buildSilentWAV(n int) []byte {
	const (
		sampleRate = 8000
		channels   = 1
		bitsPer    = 8
	)
	out := make([]byte, 0, 44+n)
	out = append(out, "RIFF"...)
	out = appendLE32(out, uint32(36+n))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = appendLE32(out, 16)         // PCM chunk size
	out = appendLE16(out, 1)          // PCM
	out = appendLE16(out, channels)   //
	out = appendLE32(out, sampleRate) //
	out = appendLE32(out, sampleRate*channels*bitsPer/8)
	out = appendLE16(out, channels*bitsPer/8)
	out = appendLE16(out, bitsPer) //
	out = append(out, "data"...)
	out = appendLE32(out, uint32(n))
	// 0x80 is silence for UNSIGNED 8-bit PCM; zero would be a loud
	// negative rail.
	out = append(out, []byte(strings.Repeat("\x80", n))...)
	return out
}

func appendLE16(b []byte, v uint16) []byte {
	return append(b, byte(v), byte(v>>8))
}

func appendLE32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// mockPNG returns a 1x1 opaque PNG. Built literally rather than
// base64-decoded at init, so a typo is a compile error.
func mockPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
		0x00, 0x00, 0x00, 0x0C, 'I', 'D', 'A', 'T',
		0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, 0x00,
		0x03, 0x01, 0x01, 0x00, 0x18, 0xDD, 0x8D, 0xB0,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
		0xAE, 0x42, 0x60, 0x82,
	}
}
