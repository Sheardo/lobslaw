package compute

import (
	"context"
	"strings"
	"testing"
)

type stubSpeak struct{ tag string }

func (s stubSpeak) Speak(context.Context, SpeakRequest) (*Artifact, error) {
	return &Artifact{Kind: ArtifactInline, Bytes: []byte(s.tag), MIME: "audio/mpeg"}, nil
}

type stubImage struct{ tag string }

func (s stubImage) Generate(context.Context, ImageRequest) (*Artifact, error) {
	return &Artifact{Kind: ArtifactInline, Bytes: []byte(s.tag), MIME: "image/png"}, nil
}

// Chat resolved its driver by name from the start; the generation
// modalities hardcoded one constructor each, which made a second
// vendor a rewrite of the wiring. These pin the registry behaviour so
// that stays fixed.
func TestGenerationDriversResolveByName(t *testing.T) {
	t.Parallel()
	s := NewDriverSet()
	s.RegisterSpeak(DriverOpenAI, func(SpeakDriverConfig) (SpeakDriver, error) {
		return stubSpeak{"default"}, nil
	})
	s.RegisterSpeak("elevenlabs", func(SpeakDriverConfig) (SpeakDriver, error) {
		return stubSpeak{"second"}, nil
	})
	s.RegisterImage(DriverOpenAI, func(ImageDriverConfig) (ImageDriver, error) {
		return stubImage{"default"}, nil
	})

	t.Run("named driver wins", func(t *testing.T) {
		t.Parallel()
		d, err := s.Speak("elevenlabs", SpeakDriverConfig{})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.Speak(context.Background(), SpeakRequest{Text: "x"})
		if string(got.Bytes) != "second" {
			t.Errorf("got %q; the named driver was not selected", got.Bytes)
		}
	})

	t.Run("empty name keeps the pre-registry behaviour", func(t *testing.T) {
		t.Parallel()
		// Configs written before driver selection existed name no
		// driver at all, and must keep resolving to the OpenAI shape.
		for _, name := range []string{"", "  ", "OpenAI"} {
			d, err := s.Speak(name, SpeakDriverConfig{})
			if err != nil {
				t.Fatalf("name %q: %v", name, err)
			}
			got, _ := d.Speak(context.Background(), SpeakRequest{Text: "x"})
			if string(got.Bytes) != "default" {
				t.Errorf("name %q resolved to %q, want the default", name, got.Bytes)
			}
		}
	})

	t.Run("unknown names list what is available", func(t *testing.T) {
		t.Parallel()
		_, err := s.Speak("elevenlab", SpeakDriverConfig{})
		if err == nil {
			t.Fatal("a typo resolved to a driver")
		}
		if !strings.Contains(err.Error(), "elevenlabs") {
			t.Errorf("error should list what IS registered, got: %v", err)
		}
		if _, err := s.Image("dall-e", ImageDriverConfig{}); err == nil {
			t.Error("an unknown image driver resolved")
		}
	})
}

// Video is the exception. The three async protocols share nothing, so
// defaulting would pick a wire format at random and fail at submit —
// after the operator's config has already been accepted. Failing at
// boot with the list is the honest outcome.
func TestJobDriverHasNoDefault(t *testing.T) {
	t.Parallel()
	s := NewDriverSet()
	s.RegisterJob("dashscope", func(JobDriverConfig) (JobDriver, error) {
		return &MockJobDriver{}, nil
	})

	_, err := s.Job("", JobDriverConfig{})
	if err == nil {
		t.Fatal("an unnamed job driver resolved to something")
	}
	if !strings.Contains(err.Error(), "dashscope") {
		t.Errorf("error should name what is available, got: %v", err)
	}
	if !strings.Contains(err.Error(), "no sensible default") {
		t.Errorf("error should explain WHY there is no default, got: %v", err)
	}

	if _, err := s.Job("dashscope", JobDriverConfig{}); err != nil {
		t.Errorf("a named job driver failed to resolve: %v", err)
	}
}

// The mock job driver is registered like any other, so a node can be
// configured for video with no vendor account and still exercise
// submit, commitment, poll and delivery.
func TestMockJobFactoryIsUsableFromTheRegistry(t *testing.T) {
	t.Parallel()
	s := NewDriverSet()
	s.RegisterJob(DriverMock, MockJobFactory)

	d, err := s.Job(DriverMock, JobDriverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := d.Submit(context.Background(), JobRequest{Prompt: "x", Modality: ModalityVideo})
	if err != nil {
		t.Fatal(err)
	}
	if !h.Valid() {
		t.Fatalf("handle %+v is not storable", h)
	}
}
