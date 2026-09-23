package apihttp

// DefaultPageLimit and MaxPageLimit are the cursor-pagination defaults
// every list endpoint uses (docs/API.md §2).
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Pagination is a resolved page size and cursor for a list endpoint.
type Pagination struct {
	Limit  int
	Cursor string
}

// ParsePagination fills in the default page size when the caller omits
// limit. The OpenAPI spec's own minimum/maximum on the limit parameter keep
// an out-of-range value from ever reaching a handler.
func ParsePagination(limit *int, cursor *string) Pagination {
	p := Pagination{Limit: DefaultPageLimit}
	if limit != nil {
		p.Limit = *limit
	}
	if p.Limit > MaxPageLimit {
		p.Limit = MaxPageLimit
	}
	if cursor != nil {
		p.Cursor = *cursor
	}
	return p
}
