package webuiapp

import (
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"net/url"
)

// Filtered collaboration links must survive navigation/reload through the real
// browser adapter, without allowing arbitrary query parameters on shell routes.
func collaborationBrowserQueryPath(base string, r *http.Request) bool {
	if r.URL == nil || r.URL.Path != base+"/collaboration" || (r.Method != "GET" && r.Method != "HEAD") || len(r.URL.RawQuery) > 2048 {
		return false
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	return e == nil && len(q) == 1 && len(q["topic"]) == 1 && (contract.CollaborationOptions{Topic: q.Get("topic")}).Validate() == nil
}
