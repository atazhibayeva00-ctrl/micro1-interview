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

	gql "github.com/graph-gophers/graphql-go"

	"micro1-interview/internal/gitea"
)

const threadCursorPrefix = "thread:"

func pullRequestNotFound(number int32) error {
	return &apiError{
		Type:    "NOT_FOUND",
		Message: fmt.Sprintf("Could not resolve to a PullRequest with the number of %d.", number),
	}
}

// PullRequest resolves a pull request by number.
func (r *repositoryResolver) PullRequest(ctx context.Context, args struct{ Number int32 }) (*pullRequestResolver, error) {
	if args.Number < 1 {
		return nil, pullRequestNotFound(args.Number)
	}
	if _, err := r.gitea.Pull(ctx, authorization(ctx), r.owner, r.name, int64(args.Number)); err != nil {
		var statusErr *gitea.StatusError
		if errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound {
			return nil, pullRequestNotFound(args.Number)
		}
		return nil, mapRepositoryError(err, r.owner, r.name)
	}
	return &pullRequestResolver{gitea: r.gitea, owner: r.owner, name: r.name, number: int64(args.Number)}, nil
}

type pullRequestResolver struct {
	gitea  *gitea.Client
	owner  string
	name   string
	number int64
}

// ReviewThreads builds threads from review comments. Gitea's API has no thread
// ID, so comments in one review on the same path and position form a thread.
func (r *pullRequestResolver) ReviewThreads(ctx context.Context, args struct {
	First int32
	After *string
}) (*reviewThreadConnection, error) {
	if args.First < 1 || args.First > maxPageSize {
		return nil, &apiError{Type: "INVALID_ARGUMENT", Message: "first must be between 1 and 100"}
	}
	after := int64(0)
	if args.After != nil {
		root, err := decodeThreadCursor(*args.After)
		if err != nil {
			return nil, &apiError{
				Type:    "INVALID_CURSOR_ARGUMENTS",
				Message: fmt.Sprintf("`%s` does not appear to be a valid cursor.", *args.After),
			}
		}
		after = root
	}
	threads, err := r.loadThreads(ctx)
	if err != nil {
		if errors.Is(err, gitea.ErrTooManyReviews) {
			return nil, unsupported(fmt.Sprintf("Listing is limited to %d reviews", gitea.MaxListedReviews))
		}
		return nil, mapRepositoryError(err, r.owner, r.name)
	}

	start := 0
	if args.After != nil {
		start = sort.Search(len(threads), func(i int) bool { return threads[i].root > after })
	}
	end := min(start+int(args.First), len(threads))
	info := &pageInfo{HasNextPage: end < len(threads), HasPreviousPage: start > 0}
	if end > start {
		first, last := encodeThreadCursor(threads[start].root), encodeThreadCursor(threads[end-1].root)
		info.StartCursor, info.EndCursor = &first, &last
	}
	return &reviewThreadConnection{Nodes: threads[start:end], PageInfo: info, TotalCount: int32(len(threads))}, nil
}

func (r *pullRequestResolver) loadThreads(ctx context.Context) ([]*reviewThread, error) {
	reviews, err := r.gitea.PullReviews(ctx, authorization(ctx), r.owner, r.name, r.number)
	if err != nil {
		return nil, err
	}
	type threadKey struct {
		review           int64
		path             string
		position         int32
		originalPosition int32
	}
	byKey := map[threadKey]*reviewThread{}
	for i := range reviews {
		review := &reviews[i]
		// A pending review request has no comments to read.
		if review.State == "REQUEST_REVIEW" {
			continue
		}
		comments, err := r.gitea.ReviewComments(ctx, authorization(ctx), r.owner, r.name, r.number, review.ID)
		if err != nil {
			return nil, err
		}
		sort.Slice(comments, func(a, b int) bool { return comments[a].ID < comments[b].ID })
		for j := range comments {
			comment := &comments[j]
			key := threadKey{review.ID, comment.Path, comment.Position, comment.OriginalPosition}
			thread, ok := byKey[key]
			if !ok {
				thread = &reviewThread{
					root:       comment.ID,
					ID:         gql.ID("PRRT_" + strconv.FormatInt(comment.ID, 10)),
					IsResolved: comment.Resolver != nil,
					IsOutdated: review.Stale,
				}
				byKey[key] = thread
			}
			thread.comments = append(thread.comments, mapReviewComment(comment))
		}
	}
	threads := make([]*reviewThread, 0, len(byKey))
	for _, thread := range byKey {
		threads = append(threads, thread)
	}
	sort.Slice(threads, func(a, b int) bool { return threads[a].root < threads[b].root })
	return threads, nil
}

func positiveLine(value int32) *int32 {
	if value < 1 {
		return nil
	}
	return &value
}

func mapReviewComment(comment *gitea.ReviewComment) *reviewComment {
	out := &reviewComment{
		ID:           gql.ID("PRRC_" + strconv.FormatInt(comment.ID, 10)),
		Body:         comment.Body,
		Path:         comment.Path,
		Line:         positiveLine(comment.Position),
		OriginalLine: positiveLine(comment.OriginalPosition),
		CreatedAt:    dateTime{comment.Created},
		UpdatedAt:    dateTime{comment.Updated},
		URL:          comment.HTMLURL,
	}
	if comment.User != nil {
		out.Author = &actor{Login: comment.User.Login}
	}
	return out
}

func encodeThreadCursor(root int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(threadCursorPrefix + strconv.FormatInt(root, 10)))
}

func decodeThreadCursor(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	value, ok := strings.CutPrefix(string(raw), threadCursorPrefix)
	if !ok {
		return 0, errors.New("unknown cursor format")
	}
	root, err := strconv.ParseInt(value, 10, 64)
	if err != nil || root < 1 {
		return 0, errors.New("invalid cursor")
	}
	return root, nil
}

type reviewThreadConnection struct {
	Nodes      []*reviewThread
	PageInfo   *pageInfo
	TotalCount int32
}

type reviewThread struct {
	root        int64
	comments    []*reviewComment
	ID          gql.ID
	IsResolved  bool
	IsOutdated  bool
	IsCollapsed bool
}

// Comments returns the first comments of the thread, oldest first.
func (t *reviewThread) Comments(args struct{ First int32 }) (*reviewCommentConnection, error) {
	if args.First < 1 || args.First > maxPageSize {
		return nil, &apiError{Type: "INVALID_ARGUMENT", Message: "first must be between 1 and 100"}
	}
	return &reviewCommentConnection{
		Nodes:      t.comments[:min(len(t.comments), int(args.First))],
		TotalCount: int32(len(t.comments)),
	}, nil
}

type reviewCommentConnection struct {
	Nodes      []*reviewComment
	TotalCount int32
}

type reviewComment struct {
	ID                gql.ID
	Body              string
	Path              string
	Line              *int32
	OriginalLine      *int32
	StartLine         *int32
	OriginalStartLine *int32
	Author            *actor
	CreatedAt         dateTime
	UpdatedAt         dateTime
	URL               string
}
