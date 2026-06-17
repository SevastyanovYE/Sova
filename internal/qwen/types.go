package qwen

type MessageInput struct {
	ID              string `json:"id"`
	SourceRef       string `json:"source_ref"`
	Kind            string `json:"kind"`
	Text            string `json:"text"`
	ExtractedText   string `json:"extracted_text,omitempty"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

type MessageDecision struct {
	ID         string   `json:"id"`
	Keep       bool     `json:"keep"`
	Importance int      `json:"importance"`
	Reason     string   `json:"reason"`
	Tags       []string `json:"tags"`
	HasEvent   bool     `json:"has_event"`
}

type BatchResult struct {
	Decisions []MessageDecision `json:"decisions"`
}

type CalibrationResult struct {
	BatchSize      int    `json:"batch_size"`
	InputMessages  int    `json:"input_messages"`
	InputChars     int    `json:"input_chars"`
	DurationMillis int64  `json:"duration_ms"`
	JSONValid      bool   `json:"json_valid"`
	Kept           int    `json:"kept"`
	Important      int    `json:"important"`
	Events         int    `json:"events"`
	Error          string `json:"error,omitempty"`
}

type EventInput struct {
	ID         string `json:"id"`
	SourceRef  string `json:"source_ref"`
	SourceLink string `json:"source_link,omitempty"`
	Text       string `json:"text"`
}

type EventCandidate struct {
	ID          string   `json:"id"`
	HasEvent    bool     `json:"has_event"`
	Title       string   `json:"title"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Location    string   `json:"location"`
	Description string   `json:"description"`
	Confidence  string   `json:"confidence"`
	Missing     []string `json:"missing"`
}

type EventExtractionResult struct {
	Events []EventCandidate `json:"events"`
}
