package llm

// MultimodalContent describes input data for multimodal summarization.
type MultimodalContent struct {
	Text       string   // Text content
	ImagePaths []string // Paths to images (will be converted to base64)
	AudioPath  string   // Path to audio file (OGG)
	VideoPath  string   // Path to video file (MP4)
}

// MultimodalSummarizer is an interface for multimodal content summarization.
type MultimodalSummarizer interface {
	// SummarizeMultimodal creates a summary of multimodal content.
	SummarizeMultimodal(model, systemPrompt string, content MultimodalContent, temperature float64) (string, error)
}
