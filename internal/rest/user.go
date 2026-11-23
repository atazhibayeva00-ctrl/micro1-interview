// Package rest serves the GitHub-compatible REST API.
package rest

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"micro1-interview/internal/gitea"
	"micro1-interview/internal/graphql"
)

// Handler serves GitHub-format routes backed by Gitea.
type Handler struct {
	gitea     *gitea.Client
	publicURL string
}

// NewHandler returns the gateway's HTTP routes. publicURL is the address
// clients use to reach the gateway, and is used in returned links.
func NewHandler(client *gitea.Client, publicURL string) http.Handler {
	h := &Handler{gitea: client, publicURL: strings.TrimRight(publicURL, "/")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /api/v3/user", h.currentUser)
	const repoPrefix = "/api/v3/repos/{owner}/{repo}"
	const repoRoute = "GET " + repoPrefix
	mux.HandleFunc(repoRoute, h.repository)
	mux.HandleFunc(repoRoute+"/branches", h.branches)
	mux.HandleFunc(repoRoute+"/git/ref/{ref...}", h.gitRef)
	mux.HandleFunc(repoRoute+"/git/trees/{sha}", h.tree)
	mux.HandleFunc(repoRoute+"/contents", h.contents)
	mux.HandleFunc(repoRoute+"/contents/{path...}", h.contents)
	mux.HandleFunc(repoRoute+"/issues/{number}", h.issue)
	mux.HandleFunc(repoRoute+"/issues/{number}/comments", h.issueComments)
	mux.HandleFunc(repoRoute+"/issues/{number}/sub_issues", h.subIssues)
	mux.HandleFunc("GET /raw/{owner}/{repo}/{sha}/{path...}", h.raw)
	mux.HandleFunc("GET "+repoPrefix+"/git/blobs/{sha}", h.blob)
	mux.HandleFunc("POST "+repoPrefix+"/git/refs", h.createRef)
	mux.HandleFunc("PUT "+repoPrefix+"/contents/{path...}", h.putContents)
	mux.HandleFunc("POST "+repoPrefix+"/pulls", h.createPull)
	mux.HandleFunc(repoRoute+"/pulls/{number}", h.pull)
	mux.HandleFunc("POST "+repoPrefix+"/pulls/{number}/requested_reviewers", h.requestReviewers)
	mux.HandleFunc(repoRoute+"/pulls/{number}/files", h.pullFiles)
	mux.HandleFunc(repoRoute+"/pulls/{number}/commits", h.pullCommits)
	mux.HandleFunc(repoRoute+"/pulls/{number}/reviews", h.pullReviews)
	mux.HandleFunc(repoRoute+"/commits/{sha}/status", h.commitStatus)
	mux.HandleFunc(repoRoute+"/commits/{sha}/check-runs", h.checkRuns)
	mux.Handle("POST /api/graphql", graphql.NewHandler(client))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	})
	return mux
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if err := h.gitea.Healthy(r.Context()); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// userResponse is GitHub's authenticated-user shape. Nil fields have no Gitea
// equivalent, or could not be read with the caller's token scopes.
type userResponse struct {
	Login             string    `json:"login"`
	ID                int64     `json:"id"`
	AvatarURL         string    `json:"avatar_url"`
	HTMLURL           string    `json:"html_url"`
	Type              string    `json:"type"`
	SiteAdmin         bool      `json:"site_admin"`
	Name              *string   `json:"name"`
	Blog              *string   `json:"blog"`
	Location          *string   `json:"location"`
	Email             *string   `json:"email"`
	Bio               *string   `json:"bio"`
	PublicRepos       *int      `json:"public_repos,omitempty"`
	Followers         int       `json:"followers"`
	Following         int       `json:"following"`
	CreatedAt         time.Time `json:"created_at"`
	TotalPrivateRepos *int      `json:"total_private_repos,omitempty"`
	OwnedPrivateRepos *int      `json:"owned_private_repos,omitempty"`
}

func (h *Handler) currentUser(w http.ResponseWriter, r *http.Request) {
	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		writeError(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	user, err := h.gitea.CurrentUser(r.Context(), authorization)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	publicRepos, err := h.optionalRepoCount(r, authorization, user.ID, false)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	privateRepos, err := h.optionalRepoCount(r, authorization, user.ID, true)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, userResponse{
		Login:             user.Login,
		ID:                user.ID,
		AvatarURL:         user.AvatarURL,
		HTMLURL:           user.HTMLURL,
		Type:              "User",
		SiteAdmin:         user.IsAdmin,
		Name:              nonEmpty(user.FullName),
		Blog:              nonEmpty(user.Website),
		Location:          nonEmpty(user.Location),
		Email:             nonEmpty(user.Email),
		Bio:               nonEmpty(user.Description),
		PublicRepos:       publicRepos,
		Followers:         user.Followers,
		Following:         user.Following,
		CreatedAt:         user.Created,
		TotalPrivateRepos: privateRepos,
		OwnedPrivateRepos: privateRepos,
	})
}

// optionalRepoCount returns nil when the token lacks repository read scope, so
// a narrowly scoped token can still read its own profile.
func (h *Handler) optionalRepoCount(r *http.Request, authorization string, uid int64, private bool) (*int, error) {
	count, err := h.gitea.CountOwnedRepos(r.Context(), authorization, uid, private)
	if err != nil {
		var statusErr *gitea.StatusError
		if errors.As(err, &statusErr) && statusErr.Code == http.StatusForbidden {
			return nil, nil
		}
		return nil, err
	}
	return &count, nil
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
