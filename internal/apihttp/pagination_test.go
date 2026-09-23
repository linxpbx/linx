package apihttp

import "testing"

func TestParsePagination(t *testing.T) {
	ten, over := 10, 500
	cursor := "abc"

	cases := []struct {
		name       string
		limit      *int
		cursor     *string
		wantLimit  int
		wantCursor string
	}{
		{"defaults", nil, nil, DefaultPageLimit, ""},
		{"explicit limit and cursor", &ten, &cursor, 10, "abc"},
		{"over max is clamped", &over, nil, MaxPageLimit, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePagination(c.limit, c.cursor)
			if got.Limit != c.wantLimit || got.Cursor != c.wantCursor {
				t.Fatalf("ParsePagination() = %+v, want limit=%d cursor=%q", got, c.wantLimit, c.wantCursor)
			}
		})
	}
}
