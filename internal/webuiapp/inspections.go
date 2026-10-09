package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/scheduleview"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

type InspectionServices struct {
	Collaboration func(context.Context, contract.CollaborationOptions) (contract.CollaborationPage, error)
	Routing       func(context.Context) (contract.ModelInspectionPage, error)
	Dependencies  func(context.Context) (contract.DependencyInventory, error)
	Logging       func(context.Context) (contract.LoggingPage, error)
	Schedules     func(context.Context) (contract.SchedulePage, error)
	Skills        func(context.Context) (app.BrowserSkillPage, error)
	Stats         func(context.Context, *usagestats.Reset) (usagestats.Snapshot, error)
	Models        func(context.Context) (contract.ModelInspectionPage, error)
	Route         func(context.Context, string) (contract.RouteInspection, error)
	Usage         func(context.Context, string) (contract.TaskUsageInspection, error)
	Tools         func(context.Context, string, string, int) (contract.ToolInspectionPage, error)
	Audits        func(context.Context, string, string, int) (contract.AuditInspectionPage, error)
	Health        func(context.Context) (contract.HealthInspection, error)
	Resources     func(context.Context) (contract.ResourceInspection, error)
	Settings      func(context.Context) (contract.SettingsInspection, error)
	Deprecation   func(context.Context, string, string, string, evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error)
}

func inspectionQueryPath(base, path string) bool {
	if path == base+"/api/v1/collaboration" {
		return true
	}
	return taskActionID(base, path, "tools") != "" || taskActionID(base, path, "audits") != "" || path == base+"/api/v1/models/deprecation"
}

func (h *Handler) serveInspectionAPI(writer http.ResponseWriter, request *http.Request) bool {
	base, path := h.basePath+"/api/v1", request.URL.Path
	switch {
	case path == base+"/dependencies":
		serveInspection(h, writer, request, "dependencies_unavailable", func(ctx context.Context) (contract.DependencyInventory, error) {
			if h.inspections.Dependencies == nil {
				return contract.DependencyInventory{}, errors.New("unavailable")
			}
			return h.inspections.Dependencies(ctx)
		})
	case path == base+"/logging":
		serveInspection(h, writer, request, "logging_unavailable", func(ctx context.Context) (contract.LoggingPage, error) {
			if h.inspections.Logging == nil {
				return contract.LoggingPage{}, errors.New("unavailable")
			}
			return h.inspections.Logging(ctx)
		})
	case path == base+"/os-schedules":
		serveInspection(h, writer, request, "schedules_unavailable", func(ctx context.Context) (scheduleview.Page, error) { return scheduleview.Read(ctx), nil })
	case path == base+"/schedules":
		serveInspection(h, writer, request, "schedules_unavailable", func(ctx context.Context) (contract.SchedulePage, error) {
			if h.inspections.Schedules == nil {
				return contract.SchedulePage{}, errors.New("unavailable")
			}
			return h.inspections.Schedules(ctx)
		})
	case path == base+"/skills":
		h.serveSkills(writer, request)
	case path == base+"/stats":
		h.serveStats(writer, request)
	case path == base+"/models/deprecation":
		h.serveDeprecationInspection(writer, request)
	case path == base+"/collaboration":
		serveInspection(h, writer, request, "collaboration_unavailable", func(ctx context.Context) (contract.CollaborationPage, error) {
			if h.inspections.Collaboration == nil {
				return contract.CollaborationPage{}, errors.New("unavailable")
			}
			values, e := url.ParseQuery(request.URL.RawQuery)
			if e != nil || len(request.URL.RawQuery) > 512 {
				return contract.CollaborationPage{}, contract.ErrContract
			}
			o := contract.CollaborationOptions{}
			for key, items := range values {
				if len(items) != 1 {
					return contract.CollaborationPage{}, contract.ErrContract
				}
				switch key {
				case "topic":
					o.Topic = items[0]
				case "before":
					o.Before, e = strconv.ParseInt(items[0], 10, 64)
					if e != nil {
						return contract.CollaborationPage{}, contract.ErrContract
					}
				default:
					return contract.CollaborationPage{}, contract.ErrContract
				}
			}
			if o.Validate() != nil {
				return contract.CollaborationPage{}, contract.ErrContract
			}
			return h.inspections.Collaboration(ctx, o)
		})
	case path == base+"/routing-grid":
		serveInspection(h, writer, request, "routing_unavailable", func(ctx context.Context) (contract.ModelInspectionPage, error) {
			if h.inspections.Routing == nil {
				return contract.ModelInspectionPage{}, errors.New("unavailable")
			}
			return h.inspections.Routing(ctx)
		}, 45*time.Second)
	case path == base+"/models":
		serveInspection(h, writer, request, "models_unavailable", func(ctx context.Context) (contract.ModelInspectionPage, error) {
			if h.inspections.Models == nil {
				return contract.ModelInspectionPage{}, errors.New("unavailable")
			}
			return h.inspections.Models(ctx)
		})
	case taskActionID(h.basePath, path, "route") != "":
		task := taskActionID(h.basePath, path, "route")
		serveInspection(h, writer, request, "route_unavailable", func(ctx context.Context) (contract.RouteInspection, error) {
			if h.inspections.Route == nil {
				return contract.RouteInspection{}, errors.New("unavailable")
			}
			return h.inspections.Route(ctx, task)
		})
	case taskActionID(h.basePath, path, "usage") != "":
		task := taskActionID(h.basePath, path, "usage")
		serveInspection(h, writer, request, "usage_unavailable", func(ctx context.Context) (contract.TaskUsageInspection, error) {
			if h.inspections.Usage == nil {
				return contract.TaskUsageInspection{}, errors.New("unavailable")
			}
			return h.inspections.Usage(ctx, task)
		})
	case taskActionID(h.basePath, path, "tools") != "":
		task := taskActionID(h.basePath, path, "tools")
		if !h.prevalidateInspectionGET(writer, request) {
			return true
		}
		after, limit, err := parseInspectionPageQuery(request.URL.RawQuery)
		if err != nil || after != "" && !canonicalToolCursor(after) {
			h.writeError(writer, request, http.StatusBadRequest, "invalid_inspection_query")
			return true
		}
		serveInspection(h, writer, request, "tools_unavailable", func(ctx context.Context) (contract.ToolInspectionPage, error) {
			if h.inspections.Tools == nil {
				return contract.ToolInspectionPage{}, errors.New("unavailable")
			}
			return h.inspections.Tools(ctx, task, after, limit)
		})
	case taskActionID(h.basePath, path, "audits") != "":
		task := taskActionID(h.basePath, path, "audits")
		if !h.prevalidateInspectionGET(writer, request) {
			return true
		}
		after, limit, err := parseInspectionPageQuery(request.URL.RawQuery)
		if err != nil || after != "" && !contract.ValidID(after) {
			h.writeError(writer, request, http.StatusBadRequest, "invalid_inspection_query")
			return true
		}
		serveInspection(h, writer, request, "audits_unavailable", func(ctx context.Context) (contract.AuditInspectionPage, error) {
			if h.inspections.Audits == nil {
				return contract.AuditInspectionPage{}, errors.New("unavailable")
			}
			return h.inspections.Audits(ctx, task, after, limit)
		})
	case path == base+"/health":
		serveInspection(h, writer, request, "health_unavailable", func(ctx context.Context) (contract.HealthInspection, error) {
			if h.inspections.Health == nil {
				return contract.HealthInspection{}, errors.New("unavailable")
			}
			return h.inspections.Health(ctx)
		})
	case path == base+"/resources":
		serveInspection(h, writer, request, "resources_unavailable", func(ctx context.Context) (contract.ResourceInspection, error) {
			if h.inspections.Resources == nil {
				return contract.ResourceInspection{}, errors.New("unavailable")
			}
			return h.inspections.Resources(ctx)
		})
	case path == base+"/settings":
		serveInspection(h, writer, request, "settings_unavailable", func(ctx context.Context) (contract.SettingsInspection, error) {
			if h.inspections.Settings == nil {
				return contract.SettingsInspection{}, errors.New("unavailable")
			}
			return h.inspections.Settings(ctx)
		})
	default:
		return false
	}
	return true
}

func (h *Handler) serveDeprecationInspection(writer http.ResponseWriter, request *http.Request) {
	if !h.prevalidateInspectionGET(writer, request) {
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values) != 6 || h.inspections.Deprecation == nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_inspection_query")
		return
	}
	one := func(name string) (string, bool) { items := values[name]; return first(items), len(items) == 1 }
	model, modelOK := one("model")
	domain, domainOK := one("domain")
	profile, profileOK := one("profile")
	windowText, windowOK := one("window")
	minText, minOK := one("min_samples")
	thresholdText, thresholdOK := one("failure_threshold")
	window, windowErr := strconv.Atoi(windowText)
	minimum, minErr := strconv.Atoi(minText)
	threshold, thresholdErr := strconv.ParseFloat(thresholdText, 64)
	policy := evaluation.DeprecationPolicy{Window: window, MinSamples: minimum, FailureThreshold: threshold}
	if !modelOK || !domainOK || !profileOK || !windowOK || !minOK || !thresholdOK || !contract.ValidModelID(model) || !contract.ValidID(domain) || !contract.ValidID(profile) || windowErr != nil || minErr != nil || thresholdErr != nil || policy.Validate() != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_inspection_query")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	report, reportErr := h.inspections.Deprecation(ctx, model, domain, profile, policy)
	if reportErr != nil || report.Validate() != nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "deprecation_unavailable")
		return
	}
	h.writeJSON(writer, http.StatusOK, report)
}

func first(values []string) string {
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func canonicalToolCursor(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n >= 0 && len(value) <= 10 && strconv.Itoa(n) == value
}

type inspectionResponse interface {
	contract.CollaborationPage | contract.DependencyInventory | contract.LoggingPage | scheduleview.Page | contract.SchedulePage | contract.ModelInspectionPage | contract.RouteInspection | contract.TaskUsageInspection | contract.ToolInspectionPage | contract.AuditInspectionPage | contract.HealthInspection | contract.ResourceInspection | contract.SettingsInspection
}

func serveInspection[T inspectionResponse](h *Handler, writer http.ResponseWriter, request *http.Request, code string, read func(context.Context) (T, error), timeouts ...time.Duration) {
	if !h.prevalidateInspectionGET(writer, request) {
		return
	}
	timeout := 5 * time.Second
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	}
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	value, err := safeInspection(ctx, read)
	if err != nil || inspectionInvalid(value) {
		h.writeError(writer, request, http.StatusServiceUnavailable, code)
		return
	}
	h.writeJSON(writer, http.StatusOK, value)
}

func (h *Handler) prevalidateInspectionGET(writer http.ResponseWriter, request *http.Request) bool {
	if !h.authenticated(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if !strictBrowserGET(request) {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func safeInspection[T inspectionResponse](ctx context.Context, read func(context.Context) (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("inspection unavailable")
		}
	}()
	return read(ctx)
}

func inspectionInvalid[T inspectionResponse](value T) bool {
	switch typed := any(value).(type) {
	case contract.CollaborationPage:
		return typed.Validate() != nil
	case contract.DependencyInventory:
		return typed.Validate() != nil
	case contract.LoggingPage:
		return typed.Validate() != nil
	case scheduleview.Page:
		return typed.Validate() != nil
	case contract.SchedulePage:
		return typed.Validate() != nil
	case contract.ModelInspectionPage:
		return typed.Validate() != nil
	case contract.RouteInspection:
		return typed.Validate() != nil
	case contract.TaskUsageInspection:
		return typed.Validate() != nil
	case contract.ToolInspectionPage:
		return typed.Validate() != nil
	case contract.AuditInspectionPage:
		return typed.Validate() != nil
	case contract.HealthInspection:
		return typed.Validate() != nil
	case contract.ResourceInspection:
		return typed.Validate() != nil
	case contract.SettingsInspection:
		return typed.Validate() != nil
	default:
		return true
	}
}

func parseInspectionPageQuery(raw string) (string, int, error) {
	if len(raw) > 4096 {
		return "", 0, contract.ErrContract
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", 0, contract.ErrContract
	}
	after, limit := "", 25
	for key, items := range values {
		if len(items) != 1 {
			return "", 0, contract.ErrContract
		}
		switch key {
		case "after":
			after = items[0]
			if len(after) > contract.MaxCursorBytes {
				return "", 0, contract.ErrContract
			}
		case "limit":
			limit, err = strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(limit) != items[0] {
				return "", 0, contract.ErrContract
			}
		default:
			return "", 0, contract.ErrContract
		}
	}
	if limit < 1 || limit > contract.MaxInspectionItems {
		return "", 0, contract.ErrContract
	}
	return after, limit, nil
}
