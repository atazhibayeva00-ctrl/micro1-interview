package graphql

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gql "github.com/graph-gophers/graphql-go"

	"micro1-interview/internal/gitea"
)

const maxPageSize = 100

type authKey struct{}

func authorization(ctx context.Context) string {
	value, _ := ctx.Value(authKey{}).(string)
	return value
}

// apiError is a GraphQL error with GitHub's top-level type field.
type apiError struct {
	Type    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

func forbidden() error {
	return &apiError{Type: "FORBIDDEN", Message: "Resource not accessible by personal access token"}
}

func repositoryNotFound(owner, name string) error {
	return &apiError{
		Type:    "NOT_FOUND",
		Message: fmt.Sprintf("Could not resolve to a Repository with the name '%s/%s'.", owner, name),
	}
}

func issueNotFound(number int32) error {
	return &apiError{
		Type:    "NOT_FOUND",
		Message: fmt.Sprintf("Could not resolve to an Issue with the number of %d.", number),
	}
}

func unsupported(message string) error {
	return &apiError{Type: "UNPROCESSABLE", Message: message}
}

// dateTime is GitHub's DateTime scalar.
type dateTime struct {
	time.Time
}

func (dateTime) ImplementsGraphQLType(name string) bool { return name == "DateTime" }

func (t *dateTime) UnmarshalGraphQL(input any) error {
	value, ok := input.(string)
	if !ok {
		return fmt.Errorf("invalid DateTime: %v", input)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("invalid DateTime: %w", err)
	}
	t.Time = parsed
	return nil
}

// bigInt is GitHub's BigInt scalar, used for database IDs beyond 32 bits.
type bigInt int64

func (bigInt) ImplementsGraphQLType(name string) bool { return name == "BigInt" }

func (b *bigInt) UnmarshalGraphQL(input any) error {
	switch value := input.(type) {
	case int32:
		*b = bigInt(value)
	case int64:
		*b = bigInt(value)
	default:
		return fmt.Errorf("invalid BigInt: %v", input)
	}
	return nil
}

type rootResolver struct {
	gitea *gitea.Client
}

func (r *rootResolver) Repository(ctx context.Context, args struct {
	Owner string
	Name  string
}) (*repositoryResolver, error) {
	if !gitea.ValidSegment(args.Owner) || !gitea.ValidSegment(args.Name) {
		return nil, repositoryNotFound(args.Owner, args.Name)
	}
	repo, err := r.gitea.Repository(ctx, authorization(ctx), args.Owner, args.Name)
	if err != nil {
		return nil, mapRepositoryError(err, args.Owner, args.Name)
	}
	return &repositoryResolver{gitea: r.gitea, owner: args.Owner, name: args.Name, repo: repo}, nil
}

func mapRepositoryError(err error, owner, name string) error {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Code {
		case http.StatusNotFound:
			return repositoryNotFound(owner, name)
		case http.StatusForbidden:
			return forbidden()
		}
	}
	return err
}

type repositoryResolver struct {
	gitea *gitea.Client
	owner string
	name  string
	repo  *gitea.Repository
}

func (r *repositoryResolver) IsPrivate() bool { return r.repo.Private }

func (r *repositoryResolver) Issue(ctx context.Context, args struct{ Number int32 }) (*issueResolver, error) {
	if args.Number < 1 {
		return nil, issueNotFound(args.Number)
	}
	issue, err := r.gitea.Issue(ctx, authorization(ctx), r.owner, r.name, int64(args.Number))
	if err != nil {
		var statusErr *gitea.StatusError
		if errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound {
			return nil, issueNotFound(args.Number)
		}
		return nil, mapRepositoryError(err, r.owner, r.name)
	}
	// GitHub's issue field does not resolve pull requests.
	if issue.PullRequest != nil {
		return nil, issueNotFound(args.Number)
	}
	return &issueResolver{issue: issue}, nil
}

type issueOrder struct {
	Field     string
	Direction string
}

type issueFilters struct {
	Since *dateTime
}

type issuesArgs struct {
	First    *int32
	After    *string
	Labels   *[]string
	States   *[]string
	OrderBy  *issueOrder
	FilterBy *issueFilters
}

func (r *repositoryResolver) Issues(ctx context.Context, args issuesArgs) (*issueConnection, error) {
	if args.Labels != nil && len(*args.Labels) > 0 {
		return nil, unsupported("Filtering issues by labels is not supported yet")
	}
	if args.FilterBy != nil && args.FilterBy.Since != nil {
		return nil, unsupported("Filtering issues by since is not supported yet")
	}
	if args.OrderBy != nil && (args.OrderBy.Field != "CREATED_AT" || args.OrderBy.Direction != "DESC") {
		return nil, unsupported("Only orderBy CREATED_AT with direction DESC is supported yet")
	}
	if args.First == nil || *args.First < 1 || *args.First > maxPageSize {
		return nil, &apiError{Type: "INVALID_ARGUMENT", Message: "first must be between 1 and 100"}
	}
	before := int64(0)
	if args.After != nil {
		number, err := decodeCursor(*args.After)
		if err != nil {
			return nil, &apiError{
				Type:    "INVALID_CURSOR_ARGUMENTS",
				Message: fmt.Sprintf("`%s` does not appear to be a valid cursor.", *args.After),
			}
		}
		before = number
	}

	issues, err := r.gitea.Issues(ctx, authorization(ctx), r.owner, r.name, giteaState(args.States))
	if err != nil {
		if errors.Is(err, gitea.ErrTooManyIssues) {
			return nil, unsupported(fmt.Sprintf("Listing is limited to %d matching issues", gitea.MaxListedIssues))
		}
		return nil, mapRepositoryError(err, r.owner, r.name)
	}

	start := 0
	if args.After != nil {
		// Issues are sorted by descending number, so new issues never shift this position.
		start = sort.Search(len(issues), func(i int) bool { return issues[i].Number < before })
	}
	end := min(start+int(*args.First), len(issues))

	nodes := make([]*issueResolver, 0, end-start)
	for i := start; i < end; i++ {
		nodes = append(nodes, &issueResolver{issue: &issues[i]})
	}
	info := &pageInfo{HasNextPage: end < len(issues), HasPreviousPage: start > 0}
	if len(nodes) > 0 {
		first, last := encodeCursor(issues[start].Number), encodeCursor(issues[end-1].Number)
		info.StartCursor, info.EndCursor = &first, &last
	}
	return &issueConnection{Nodes: nodes, PageInfo: info, TotalCount: int32(len(issues))}, nil
}

func giteaState(states *[]string) string {
	if states == nil {
		return "all"
	}
	open, closed := false, false
	for _, state := range *states {
		open = open || state == "OPEN"
		closed = closed || state == "CLOSED"
	}
	switch {
	case open && !closed:
		return "open"
	case closed && !open:
		return "closed"
	}
	return "all"
}

const cursorPrefix = "issue:"

func encodeCursor(number int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(number, 10)))
}

func decodeCursor(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	value, ok := strings.CutPrefix(string(raw), cursorPrefix)
	if !ok {
		return 0, errors.New("unknown cursor format")
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 1 {
		return 0, errors.New("invalid cursor number")
	}
	return number, nil
}

type issueConnection struct {
	Nodes      []*issueResolver
	PageInfo   *pageInfo
	TotalCount int32
}

type pageInfo struct {
	HasNextPage     bool
	HasPreviousPage bool
	StartCursor     *string
	EndCursor       *string
}

type issueResolver struct {
	issue *gitea.Issue
}

func (r *issueResolver) Number() int32       { return int32(r.issue.Number) }
func (r *issueResolver) Title() string       { return r.issue.Title }
func (r *issueResolver) Body() string        { return r.issue.Body }
func (r *issueResolver) State() string       { return strings.ToUpper(r.issue.State) }
func (r *issueResolver) CreatedAt() dateTime { return dateTime{r.issue.Created} }
func (r *issueResolver) UpdatedAt() dateTime { return dateTime{r.issue.Updated} }
func (r *issueResolver) DatabaseId() *bigInt { //nolint:revive // Name must match the schema.
	id := bigInt(r.issue.ID)
	return &id
}

func (r *issueResolver) Author() *actor {
	if r.issue.User == nil {
		return nil
	}
	return &actor{Login: r.issue.User.Login}
}

func (r *issueResolver) Labels(args struct{ First int32 }) (*labelConnection, error) {
	if args.First < 1 || args.First > maxPageSize {
		return nil, &apiError{Type: "INVALID_ARGUMENT", Message: "first must be between 1 and 100"}
	}
	labels := r.issue.Labels
	nodes := make([]*label, 0, min(len(labels), int(args.First)))
	for _, item := range labels[:min(len(labels), int(args.First))] {
		node := &label{
			ID:    gql.ID("L_" + strconv.FormatInt(item.ID, 10)),
			Name:  item.Name,
			Color: strings.TrimPrefix(item.Color, "#"),
		}
		if item.Description != "" {
			description := item.Description
			node.Description = &description
		}
		nodes = append(nodes, node)
	}
	return &labelConnection{Nodes: nodes, TotalCount: int32(len(labels))}, nil
}

func (r *issueResolver) Assignees(args struct{ First int32 }) (*userConnection, error) {
	if args.First < 1 || args.First > maxPageSize {
		return nil, &apiError{Type: "INVALID_ARGUMENT", Message: "first must be between 1 and 100"}
	}
	assignees := r.issue.Assignees
	nodes := make([]*user, 0, min(len(assignees), int(args.First)))
	for _, item := range assignees[:min(len(assignees), int(args.First))] {
		nodes = append(nodes, &user{Login: item.Login})
	}
	return &userConnection{Nodes: nodes}, nil
}

func (r *issueResolver) Comments() *commentConnection {
	return &commentConnection{TotalCount: int32(r.issue.Comments)}
}

type actor struct {
	Login string
}

type user struct {
	Login string
}

type userConnection struct {
	Nodes []*user
}

type label struct {
	ID          gql.ID
	Name        string
	Color       string
	Description *string
}

type labelConnection struct {
	Nodes      []*label
	TotalCount int32
}

type commentConnection struct {
	TotalCount int32
}
