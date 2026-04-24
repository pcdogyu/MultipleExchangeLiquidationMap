package liqmap

type ModelConfigPageData struct {
	ModelConfig
	PageTitle        string
	ActiveMenu       string
	ShowAnalysisInfo bool
}

type BadRequestError struct {
	Message string
}

func (e BadRequestError) Error() string {
	return e.Message
}
