package liqmap

import (
	"fmt"
	"strings"
	"time"
)

type HTTPStatusError struct {
	URL        string
	Status     string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("http %s", e.Status)
	}
	return fmt.Sprintf("http %s: %s", e.Status, strings.TrimSpace(e.Body))
}

type ExchangePausedError struct {
	Exchange string
	Until    time.Time
	Reason   string
}

func (e *ExchangePausedError) Error() string {
	if e == nil {
		return ""
	}
	untilText := e.Until.Local().Format("2006-01-02 15:04:05")
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		return fmt.Sprintf("%s access paused until %s after repeated identical access errors: %s",
			e.Exchange, untilText, reason)
	}
	return fmt.Sprintf("%s access paused until %s after repeated identical access errors",
		e.Exchange, untilText)
}

type ExchangeAPIGuard struct {
	ConsecutiveHTTPError int
	LastHTTPErrorKey     string
	LastHTTPErrorText    string
	PausedUntil          time.Time
	PauseReason          string
}
