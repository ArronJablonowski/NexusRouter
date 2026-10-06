package webui

import "time"

// LoggingPage exposes storage metadata only, never record contents or credentials.
type LoggingPage struct {
	Version    int           `json:"version"`
	ObservedAt time.Time     `json:"observed_at"`
	Items      []LogLocation `json:"items"`
}
type LogLocation struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Location string   `json:"location"`
	Scope    string   `json:"scope"`
	Format   string   `json:"format"`
	Status   string   `json:"status"`
	Records  []string `json:"records"`
	Note     string   `json:"note"`
}

func (p LoggingPage) Validate() error {
	if p.Version != 1 || !validBrowserTime(p.ObservedAt) || len(p.Items) > 32 {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, x := range p.Items {
		if !validID(x.ID) || seen[x.ID] || len(x.Records) == 0 || len(x.Records) > 16 {
			return ErrContract
		}
		seen[x.ID] = true
		for _, v := range []string{x.Name, x.Location, x.Scope, x.Format, x.Status, x.Note} {
			if requireText(v, 4096) != nil {
				return ErrContract
			}
		}
		for _, v := range x.Records {
			if requireText(v, 512) != nil {
				return ErrContract
			}
		}
	}
	return nil
}
