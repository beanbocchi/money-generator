package newsgate

// The log is append-only JSONL, one record per line. Each type below is one record
// kind; encoding/json writes the fields in struct order, which is the order the log
// has always had, with ts last.

// Stamp is the time a record was written, embedded as the last field of every record.
type Stamp struct {
	TS string `json:"ts"`
}

func (s *Stamp) setTS(ts string) { s.TS = ts }

type record interface{ setTS(string) }

// StartRecord is written once per process start.
type StartRecord struct {
	Kind  string `json:"kind"` // "start"
	Model string `json:"model"`
	Cycle int    `json:"cycle"`
	Stamp
}

// CycleRecord is the heartbeat at the end of every completed cycle.
type CycleRecord struct {
	Kind        string `json:"kind"` // "cycle"
	Fetched     int    `json:"fetched"`
	New         int    `json:"new"`
	Old         int    `json:"old"`
	Classified  int    `json:"classified"`
	FetchErrors int    `json:"fetch_errors"`
	APIErrors   int    `json:"api_errors"`
	Stamp
}

// CallRecord is one successful model call.
type CallRecord struct {
	Kind       string `json:"kind"` // "call"
	Items      int    `json:"items"`
	Classified int    `json:"classified"`
	Meta
	Stamp
}

// NoResultRecord is a call that returned no usable answer.
type NoResultRecord struct {
	Kind  string   `json:"kind"` // "no_result"
	Items []string `json:"items"`
	Meta
	Refusal *string `json:"refusal"`
	Stamp
}

// ItemRecord is one classified headline, with its window if it opened one.
type ItemRecord struct {
	Kind string `json:"kind"` // "item"
	Item
	Fetched   string   `json:"fetched"`
	Decided   string   `json:"decided"`
	EventType string   `json:"event_type"`
	Scope     string   `json:"scope"`
	Tickers   []string `json:"tickers"`
	Severity  int      `json:"severity"`
	Reason    string   `json:"reason"`
	Window    *Window  `json:"window,omitempty"`
	Stamp
}

// SkippedOldRecord is a headline first seen more than MaxAgeHours after publication.
type SkippedOldRecord struct {
	Kind    string `json:"kind"` // "skipped_old"
	Fetched string `json:"fetched"`
	Item
	Stamp
}

// FetchErrorRecord is a source that could not be read in a cycle.
type FetchErrorRecord struct {
	Kind   string `json:"kind"` // "fetch_error"
	Source string `json:"source"`
	Error  string `json:"error"`
	Stamp
}

// APIErrorRecord is a failed model call; its items are asked about again next cycle.
type APIErrorRecord struct {
	Kind  string `json:"kind"` // "api_error"
	Error string `json:"error"`
	Items int    `json:"items"`
	Stamp
}

// CycleErrorRecord is a cycle that failed as a whole.
type CycleErrorRecord struct {
	Kind  string `json:"kind"` // "cycle_error"
	Error string `json:"error"`
	Stamp
}

// errorText is an error's message cut to 300 characters.
func errorText(err error) string {
	return truncateRunes(err.Error(), 300)
}
