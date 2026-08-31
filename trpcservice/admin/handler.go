package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storagemigration"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// Handler exposes the configuration administration HTTP API.
type Handler struct {
	service  *Service
	redactor *servicelog.Redactor
}

// NewHandler creates an HTTP handler. Authentication must be applied by the
// caller, for example with Authenticator.Wrap.
func NewHandler(service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("admin: nil service")
	}
	return &Handler{service: service, redactor: servicelog.NewRedactor(nil, nil)}, nil
}

// ServeHTTP handles validate, publish, version list, current version, and
// rollback operations. The tenant scope always comes from the URL path, which
// the authentication layer has already authorized.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "tenants" {
		http.NotFound(writer, request)
		return
	}
	tenantID := parts[2]
	if parts[3] == "storage" {
		handler.serveStorage(writer, request, tenantID, parts)
		return
	}
	if parts[3] != "configs" {
		http.NotFound(writer, request)
		return
	}
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
			handler.writeError(writer, http.StatusUnprocessableEntity, err)
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
			handler.writeServiceError(writer, err)
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
			handler.writeServiceError(writer, err)
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
			handler.writeServiceError(writer, err)
			return
		}
		result := make([]map[string]any, len(records))
		for i := range records {
			result[i] = metadata(records[i])
		}
		writeJSON(writer, http.StatusOK, result)
	case "current":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		record, err := handler.service.Current(request.Context(), tenantID)
		if err != nil {
			handler.writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, metadata(record))
	default:
		http.NotFound(writer, request)
	}
}

func (handler *Handler) serveStorage(writer http.ResponseWriter, request *http.Request, tenantID string, parts []string) {
	if len(parts) < 5 || parts[4] != "migrations" {
		http.NotFound(writer, request)
		return
	}
	if len(parts) == 5 {
		switch request.Method {
		case http.MethodGet:
			jobs, err := handler.service.ListMigrations(request.Context(), tenantID)
			if err != nil {
				handler.writeMigrationError(writer, err)
				return
			}
			result := make([]map[string]any, len(jobs))
			for index := range jobs {
				result[index] = migrationMetadata(jobs[index])
			}
			writeJSON(writer, http.StatusOK, result)
		case http.MethodPost:
			var body struct {
				AppID    string                  `json:"app_id"`
				Domain   storagemigration.Domain `json:"domain"`
				Expected tenant.ConfigVersion    `json:"expected_version"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<16))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&body); err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
			job, err := handler.service.PlanMigration(request.Context(), tenantID, body.AppID, body.Domain, body.Expected)
			if err != nil {
				handler.writeMigrationError(writer, err)
				return
			}
			writeJSON(writer, http.StatusCreated, migrationMetadata(job))
		default:
			methodNotAllowed(writer)
		}
		return
	}
	jobID := parts[5]
	if len(parts) == 6 {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		job, err := handler.service.GetMigration(request.Context(), tenantID, jobID)
		if err != nil {
			handler.writeMigrationError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, migrationMetadata(job))
		return
	}
	if len(parts) == 7 && parts[6] == "cancel" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		if err := handler.service.CancelMigration(request.Context(), tenantID, jobID); err != nil {
			handler.writeMigrationError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"tenant_id": tenantID, "migration_id": jobID, "status": storagemigration.StatusCanceled})
		return
	}
	http.NotFound(writer, request)
}

func migrationMetadata(job storagemigration.Job) map[string]any {
	result := map[string]any{"tenant_id": job.TenantID, "migration_id": job.JobID, "app_id": job.AppID, "config_version": job.ConfigVersion, "domain": job.Domain, "status": job.Status, "source_rows": job.SourceRows, "copied_rows": job.CopiedRows, "attempts": job.Attempts, "created_by": job.CreatedBy, "created_at": job.CreatedAt, "updated_at": job.UpdatedAt}
	if job.LastErrorType != "" {
		result["error_type"] = job.LastErrorType
	}
	if job.CompletedAt != nil {
		result["completed_at"] = *job.CompletedAt
	}
	return result
}

func (handler *Handler) writeMigrationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrVersionConflict), errors.Is(err, storagemigration.ErrConflict):
		handler.writeError(writer, http.StatusConflict, err)
	case errors.Is(err, storagemigration.ErrNotFound):
		handler.writeError(writer, http.StatusNotFound, err)
	case errors.Is(err, ErrInvalidConfig):
		handler.writeError(writer, http.StatusUnprocessableEntity, err)
	default:
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
	}
}

func readPayload(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return nil, false
	}
	return payload, true
}

func versionParameter(writer http.ResponseWriter, request *http.Request, name string) (tenant.ConfigVersion, bool) {
	value, err := strconv.ParseUint(request.URL.Query().Get(name), 10, 64)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": name + " must be an unsigned integer"})
		return 0, false
	}
	return tenant.ConfigVersion(value), true
}

// metadata is the only API projection of a config version. It intentionally
// excludes the payload so SecretRef material and internal config never leave
// the control plane through the Admin API.
func metadata(record repository.ConfigRecord) map[string]any {
	result := map[string]any{"tenant_id": record.TenantID, "version": record.Version, "content_hash": record.SHA256, "created_by": record.CreatedBy, "published_at": record.CreatedAt}
	if record.RolledBackFrom != nil {
		result["rollback_of"] = *record.RolledBackFrom
	}
	return result
}

func (handler *Handler) writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrVersionConflict):
		handler.writeError(writer, http.StatusConflict, err)
	case errors.Is(err, repository.ErrNotFound):
		handler.writeError(writer, http.StatusNotFound, err)
	case errors.Is(err, ErrInvalidConfig):
		handler.writeError(writer, http.StatusUnprocessableEntity, err)
	default:
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
	}
}
func methodNotAllowed(writer http.ResponseWriter) {
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

// writeError redacts error text so resolved secrets can never leak through
// HTTP error responses.
func (handler *Handler) writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": handler.redactor.RedactString(err.Error())})
}

// writeError is used by the authentication layer before a Handler exists.
func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": servicelog.NewRedactor(nil, nil).RedactString(err.Error())})
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
