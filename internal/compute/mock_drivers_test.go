package compute

import (
	"bytes"
	"context"
	"image/png"
	"testing"
)

// R22 asks that a node whose every provider is `driver = "mock"` boots
// with no network access and serves a full turn. That needs a mock for
// EVERY modality — a missing one is a node that boots and then fails
// the moment somebody asks it to say something out loud.
//
// It is also the cheapest check that the driver seam is complete: a
// modality with no mock is one whose wire shape has not really been
// separated from its plumbing.

func TestEveryModalityHasAMock(t *testing.T) {
	t.Parallel()
	s := NewDriverSet()
	s.RegisterChat(DriverMock, MockChatFactory)
	s.RegisterVision(DriverMock, MockVisionFactory)
	s.RegisterAudio(DriverMock, MockAudioFactory)
	s.RegisterEmbedding(DriverMock, MockEmbeddingFactory)
	s.RegisterSpeak(DriverMock, MockSpeakFactory)
	s.RegisterImage(DriverMock, MockImageFactory)
	s.RegisterJob(DriverMock, MockJobFactory)

	if _, err := s.Chat(DriverMock, ChatDriverConfig{}); err != nil {
		t.Errorf("chat: %v", err)
	}
	if _, err := s.Vision(DriverMock, VisionDriverConfig{}); err != nil {
		t.Errorf("vision: %v", err)
	}
	if _, err := s.Audio(DriverMock, AudioDriverConfig{}); err != nil {
		t.Errorf("audio: %v", err)
	}
	if _, err := s.EmbeddingFactory(DriverMock); err != nil {
		t.Errorf("embedding: %v", err)
	}
	if _, err := s.Speak(DriverMock, SpeakDriverConfig{}); err != nil {
		t.Errorf("speak: %v", err)
	}
	if _, err := s.Image(DriverMock, ImageDriverConfig{}); err != nil {
		t.Errorf("image: %v", err)
	}
	if _, err := s.Job(DriverMock, JobDriverConfig{}); err != nil {
		t.Errorf("job: %v", err)
	}
}

// A mock that produced empty bytes would exercise none of the delivery
// path it exists to let somebody exercise.

// The WAV must actually parse. The artifact path forwards this to a
// channel, and Telegram rejects a voice note whose container it cannot
// read — a mock producing garbage would make the delivery path
// untestable exactly where it is fiddliest.
func TestTheMockSpeechIsAValidWAV(t *testing.T) {
	t.Parallel()
	d, err := MockSpeakFactory(SpeakDriverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Speak(context.Background(), SpeakRequest{Text: "hello", Format: "wav"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ArtifactInline {
		t.Errorf("kind = %q", got.Kind)
	}
	if len(got.Bytes) < 44 {
		t.Fatalf("%d bytes; too short to be a WAV header", len(got.Bytes))
	}
	if !bytes.HasPrefix(got.Bytes, []byte("RIFF")) || !bytes.Contains(got.Bytes[:16], []byte("WAVE")) {
		t.Errorf("not a RIFF/WAVE container: % x", got.Bytes[:16])
	}
	if got.MIME != "audio/wav" {
		t.Errorf("MIME = %q, want audio/wav", got.MIME)
	}
}

// The requested container is honoured in the MIME type even though the
// bytes are always WAV: returning audio/mpeg for a "wav" request would
// send somebody chasing a bug that is not there.
func TestTheMockSpeechHonoursTheRequestedFormatInItsMIME(t *testing.T) {
	t.Parallel()
	d, _ := MockSpeakFactory(SpeakDriverConfig{})
	for format, want := range map[string]string{
		"wav":  "audio/wav",
		"opus": "audio/ogg",
		"mp3":  "audio/mpeg",
		"":     "audio/wav",
	} {
		got, err := d.Speak(context.Background(), SpeakRequest{Text: "x", Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if got.MIME != want {
			t.Errorf("format %q gave MIME %q, want %q", format, got.MIME, want)
		}
	}
}

// The PNG must decode, for the same reason the WAV must parse.
func TestTheMockImageIsAValidPNG(t *testing.T) {
	t.Parallel()
	d, err := MockImageFactory(ImageDriverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Generate(context.Background(), ImageRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ArtifactInline || got.MIME != "image/png" {
		t.Errorf("artifact = %+v", got)
	}
	img, err := png.Decode(bytes.NewReader(got.Bytes))
	if err != nil {
		t.Fatalf("the mock PNG does not decode: %v", err)
	}
	if img.Bounds().Empty() {
		t.Error("the mock PNG has no pixels")
	}
}

// No mock may reach the network. That is the whole point of the box —
// a node configured entirely with mocks must serve a turn with no
// egress at all.
func TestNoMockDriverHasAnEndpoint(t *testing.T) {
	t.Parallel()
	// Built with an empty config: a driver that needed an endpoint
	// would refuse here, and one that silently kept a default would
	// try to reach it at call time.
	if _, err := MockSpeakFactory(SpeakDriverConfig{}); err != nil {
		t.Errorf("speak mock demanded configuration: %v", err)
	}
	if _, err := MockImageFactory(ImageDriverConfig{}); err != nil {
		t.Errorf("image mock demanded configuration: %v", err)
	}
	if _, err := MockVisionFactory(VisionDriverConfig{}); err != nil {
		t.Errorf("vision mock demanded configuration: %v", err)
	}
	if _, err := MockAudioFactory(AudioDriverConfig{}); err != nil {
		t.Errorf("audio mock demanded configuration: %v", err)
	}
	if _, err := MockEmbeddingFactory(EmbeddingDriverConfig{}); err != nil {
		t.Errorf("embedding mock demanded configuration: %v", err)
	}
}
