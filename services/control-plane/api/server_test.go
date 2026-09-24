package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte("openapi: 3.1.0\ninfo:\n  title: test\n  version: \"1\"\npaths: {}\n"))
	if err != nil {
		t.Fatalf("loading test spec: %v", err)
	}
	return NewServer(spec, nil, nil, nil, nil)
}

func TestGetOpenapiSpec(t *testing.T) {
	resp, err := testServer(t).GetOpenapiSpec(context.Background(), GetOpenapiSpecRequestObject{})
	if err != nil {
		t.Fatalf("GetOpenapiSpec: %v", err)
	}
	doc, ok := resp.(GetOpenapiSpec200JSONResponse)
	if !ok {
		t.Fatalf("response type = %T, want GetOpenapiSpec200JSONResponse", resp)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi field = %v, want 3.1.0", doc["openapi"])
	}
}

func TestGetMeWithoutPrincipal(t *testing.T) {
	resp, err := testServer(t).GetMe(context.Background(), GetMeRequestObject{})
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	problem, ok := resp.(GetMedefaultApplicationProblemPlusJSONResponse)
	if !ok {
		t.Fatalf("response type = %T, want the problem response", resp)
	}
	if problem.StatusCode != http.StatusInternalServerError || problem.Body.Code != "internal" {
		t.Fatalf("unexpected problem: %+v", problem)
	}
}

func TestGetMeWithPrincipal(t *testing.T) {
	want := Principal{Id: "cli", Type: PrincipalTypeSystem, Scopes: auth.Scopes}
	ctx := auth.WithPrincipal(context.Background(), auth.SystemPrincipal(uuid.Nil))

	resp, err := testServer(t).GetMe(ctx, GetMeRequestObject{})
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	got, ok := resp.(GetMe200JSONResponse)
	if !ok {
		t.Fatalf("response type = %T, want GetMe200JSONResponse", resp)
	}
	if got.Id != want.Id || got.Type != want.Type {
		t.Fatalf("GetMe() = %+v, want %+v", got, want)
	}
}

func TestListEventTypesDefaultReturnsEverything(t *testing.T) {
	resp, err := testServer(t).ListEventTypes(context.Background(), ListEventTypesRequestObject{})
	if err != nil {
		t.Fatalf("ListEventTypes: %v", err)
	}
	list, ok := resp.(ListEventTypes200JSONResponse)
	if !ok {
		t.Fatalf("response type = %T, want ListEventTypes200JSONResponse", resp)
	}
	if len(list.Items) != len(eventTypes) {
		t.Fatalf("got %d items, want %d", len(list.Items), len(eventTypes))
	}
	if list.NextCursor != nil {
		t.Fatalf("next_cursor = %q, want none on the last page", *list.NextCursor)
	}
}

func TestListEventTypesPaginates(t *testing.T) {
	s := testServer(t)
	limit := 5

	first, err := s.ListEventTypes(context.Background(), ListEventTypesRequestObject{
		Params: ListEventTypesParams{Limit: &limit},
	})
	if err != nil {
		t.Fatalf("ListEventTypes (page 1): %v", err)
	}
	firstList := first.(ListEventTypes200JSONResponse)
	if len(firstList.Items) != limit {
		t.Fatalf("page 1 got %d items, want %d", len(firstList.Items), limit)
	}
	if firstList.NextCursor == nil {
		t.Fatal("page 1 should carry a next_cursor")
	}
	if firstList.Items[0].Name != eventTypes[0].Name {
		t.Fatalf("page 1 first item = %q, want %q", firstList.Items[0].Name, eventTypes[0].Name)
	}

	second, err := s.ListEventTypes(context.Background(), ListEventTypesRequestObject{
		Params: ListEventTypesParams{Limit: &limit, Cursor: firstList.NextCursor},
	})
	if err != nil {
		t.Fatalf("ListEventTypes (page 2): %v", err)
	}
	secondList := second.(ListEventTypes200JSONResponse)
	if secondList.Items[0].Name != eventTypes[limit].Name {
		t.Fatalf("page 2 first item = %q, want %q", secondList.Items[0].Name, eventTypes[limit].Name)
	}
}

func TestListEventTypesInvalidCursor(t *testing.T) {
	cursor := "not-a-valid-cursor!!"
	resp, err := testServer(t).ListEventTypes(context.Background(), ListEventTypesRequestObject{
		Params: ListEventTypesParams{Cursor: &cursor},
	})
	if err != nil {
		t.Fatalf("ListEventTypes: %v", err)
	}
	problem, ok := resp.(ListEventTypesdefaultApplicationProblemPlusJSONResponse)
	if !ok {
		t.Fatalf("response type = %T, want the problem response", resp)
	}
	if problem.StatusCode != http.StatusBadRequest || problem.Body.Code != "cursor_invalid" {
		t.Fatalf("unexpected problem: %+v", problem)
	}
}
