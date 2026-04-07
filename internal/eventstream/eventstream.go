package eventstream

import "time"

type Event struct {
	Source     string
	Kind       string
	Message    string
	ScriptPath string
	Package    string
	Current    int
	Total      int
	Duration   string
	Fields     map[string]string
}

type Handler func(Event)

func Emit(handler Handler, event Event) {
	if handler == nil {
		return
	}
	handler(event)
}

func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
