package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// Handler exposes the minimal configuration administration HTTP API.
type Handler struct{ service *Service }

// NewHandler creates an HTTP handler. Authentication must be applied by the caller.
func NewHandler(service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("admin: nil service")
	}
	return &Handler{service: service}, nil
}

// ServeHTTP handles validate, publish, version-list, and rollback operations.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "tenants" || parts[3] != "configs" {
		http.NotFound(writer, request)
		return
	}
	tenantID := parts[2]
	action := "list"
	if len(parts) == 5 {
		action = parts[4]
	} else if len(parts) != 4 {
		http.NotFound(writer, request)
		return
	}
	switch action {
	case "validate":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		payload, ok := readPayload(writer, request)
		if !ok {
			return
		}
		file, err := handler.service.Validate(payload)
		if err != nil || file.Tenants[0].ID != tenantID {
			if err == nil {
				err = errors.New("tenant scope does not match payload")
			}
			writeError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"valid": true})
	case "publish":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		expected, ok := versionParameter(writer, request, "expected_version")
		if !ok {
			return
		}
		payload, ok := readPayload(writer, request)
		if !ok {
			return
		}
		record, err := handler.service.Publish(request.Context(), tenantID, expected, payload)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, metadata(record))
	case "rollback":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		expected, ok := versionParameter(writer, request, "expected_version")
		if !ok {
			return
		}
		target, ok := versionParameter(writer, request, "target_version")
		if !ok {
			return
		}
		record, err := handler.service.Rollback(request.Context(), tenantID, expected, target)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, metadata(record))
	case "list":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		records, err := handler.service.Versions(request.Context(), tenantID)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		result := make([]map[string]any, len(records))
		for i := range records {
			result[i] = metadata(records[i])
		}
		writeJSON(writer, http.StatusOK, result)
	default:
		http.NotFound(writer, request)
	}
}

func readPayload(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("invalid request body"))
		return nil, false
	}
	return payload, true
}

func versionParameter(writer http.ResponseWriter, request *http.Request, name string) (tenant.ConfigVersion, bool) {
	value, err := strconv.ParseUint(request.URL.Query().Get(name), 10, 64)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New(name+" must be an unsigned integer"))
		return 0, false
	}
	return tenant.ConfigVersion(value), true
}

func metadata(record repository.ConfigRecord) map[string]any {
	result := map[string]any{"tenant_id": record.TenantID, "version": record.Version, "sha256": record.SHA256, "created_at": record.CreatedAt}
	if record.RolledBackFrom != nil {
		result["rolled_back_from"] = *record.RolledBackFrom
	}
	return result
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrVersionConflict):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, repository.ErrNotFound):
		writeError(writer, http.StatusNotFound, err)
	default:
		writeError(writer, http.StatusUnprocessableEntity, err)
	}
}
func methodNotAllowed(writer http.ResponseWriter) {
	writeError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
