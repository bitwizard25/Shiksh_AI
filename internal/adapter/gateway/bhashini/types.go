package bhashini

// Wire types of Bhashini's ULCA pipeline API. They never leave this package.

type language struct {
	SourceLanguage string `json:"sourceLanguage"`
}

type taskConfig struct {
	Language     language `json:"language"`
	ServiceID    string   `json:"serviceId,omitempty"`
	AudioFormat  string   `json:"audioFormat,omitempty"`
	SamplingRate int      `json:"samplingRate,omitempty"`
	Gender       string   `json:"gender,omitempty"`
}

type pipelineTask struct {
	TaskType string     `json:"taskType"`
	Config   taskConfig `json:"config"`
}

type pipelineRequestConfig struct {
	PipelineID string `json:"pipelineId"`
}

type configRequest struct {
	PipelineTasks         []pipelineTask        `json:"pipelineTasks"`
	PipelineRequestConfig pipelineRequestConfig `json:"pipelineRequestConfig"`
}

type serviceConfig struct {
	ServiceID string   `json:"serviceId"`
	Language  language `json:"language"`
}

type taskServices struct {
	TaskType string          `json:"taskType"`
	Config   []serviceConfig `json:"config"`
}

type apiKey struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type inferenceEndpoint struct {
	CallbackURL     string `json:"callbackUrl"`
	InferenceAPIKey apiKey `json:"inferenceApiKey"`
}

type configResponse struct {
	PipelineResponseConfig       []taskServices    `json:"pipelineResponseConfig"`
	PipelineInferenceAPIEndPoint inferenceEndpoint `json:"pipelineInferenceAPIEndPoint"`
}

type textInput struct {
	Source *string `json:"source"` // null for ASR, the text for TTS
}

type audioInput struct {
	AudioContent string `json:"audioContent"`
}

type inputData struct {
	Input []textInput  `json:"input,omitempty"`
	Audio []audioInput `json:"audio,omitempty"`
}

type computeRequest struct {
	PipelineTasks []pipelineTask `json:"pipelineTasks"`
	InputData     inputData      `json:"inputData"`
}

type textOutput struct {
	Source string `json:"source"`
}

type audioConfig struct {
	AudioFormat  string `json:"audioFormat,omitempty"`
	SamplingRate int    `json:"samplingRate,omitempty"`
}

type taskResponse struct {
	TaskType string       `json:"taskType"`
	Output   []textOutput `json:"output,omitempty"`
	Audio    []audioInput `json:"audio,omitempty"`
	Config   *audioConfig `json:"config,omitempty"`
}

type computeResponse struct {
	PipelineResponse []taskResponse `json:"pipelineResponse"`
}
