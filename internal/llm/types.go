package llm

import "context"

// MultimodalContent describes input data for multimodal summarization.
type MultimodalContent struct {
	Text       string   // Text content
	ImagePaths []string // Paths to images (will be converted to base64)
	ImageURLs  []string // Immutable encoded images retained across decision retries
	AudioPath  string   // Path to audio file (OGG)
	VideoPath  string   // Path to video file (MP4)
}

type MultimodalDecider interface {
	DecideMultimodal(ctx context.Context, model, systemPrompt string, content MultimodalContent, temperature float64) (string, error)
}

// MultimodalSummarizer is an interface for multimodal content summarization.
type MultimodalSummarizer interface {
	// SummarizeMultimodal creates a summary of multimodal content.
	SummarizeMultimodal(ctx context.Context, model, systemPrompt string, content MultimodalContent, temperature float64) (string, error)
}
