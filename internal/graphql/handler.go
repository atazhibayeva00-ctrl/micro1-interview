package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	gql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"

	"micro1-interview/internal/gitea"
)

const (
	maxRequestBytes = 64 << 10
	maxQueryBytes   = 16 << 10
	maxQueryDepth   = 8
	// maxSelections also limits aliases, which could otherwise repeat Gitea calls.
	maxSelections = 100
)

type handler struct {
	schema *gql.Schema
}

// NewHandler returns the GraphQL endpoint.
func NewHandler(client *gitea.Client) http.Handler {
	schema := gql.MustParseSchema(schemaText, &rootResolver{gitea: client},
		gql.UseFieldResolvers(),
		gql.MaxDepth(maxQueryDepth),
		gql.MaxSelections(maxSelections),
		gql.MaxQueryLength(maxQueryBytes),
		gql.DisableIntrospection(),
	)
	return &handler{schema: schema}
}

type request struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// responseError is GitHub's GraphQL error shape.
type responseError struct {
	Type      string               `json:"type,omitempty"`
	Path      []any                `json:"path,omitempty"`
	Locations []gqlerrors.Location `json:"locations,omitempty"`
	Message   string               `json:"message"`
}

type response struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []responseError `json:"errors,omitempty"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		writeMessage(w, http.StatusUnauthorized, "This endpoint requires you to be authenticated.")
		return
	}
	var body request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeMessage(w, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		writeMessage(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}

	ctx := context.WithValue(r.Context(), authKey{}, token)
	result := h.schema.Exec(ctx, body.Query, body.OperationName, body.Variables)

	out := response{Data: result.Data}
	for _, queryErr := range result.Errors {
		item := responseError{Path: queryErr.Path, Locations: queryErr.Locations, Message: queryErr.Message}
		if queryErr.ResolverError != nil {
			var api *apiError
			if !errors.As(queryErr.ResolverError, &api) {
				writeUpstreamFailure(w, queryErr.ResolverError)
				return
			}
			item.Type = api.Type
			item.Message = api.Message
		}
		out.Errors = append(out.Errors, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// writeUpstreamFailure reports Gitea failures without exposing their details.
func writeUpstreamFailure(w http.ResponseWriter, err error) {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) && statusErr.Code == http.StatusUnauthorized {
		writeMessage(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	slog.Error("graphql resolver failed", "error", err)
	writeMessage(w, http.StatusBadGateway, "Bad Gateway")
}

func writeMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message, "status": strconv.Itoa(status)})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response", "error", err)
	}
}
