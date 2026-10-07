package api

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/trunk"
)

func toNumberCategories(categories []string) []NumberCategory {
	out := make([]NumberCategory, len(categories))
	for i, c := range categories {
		out[i] = NumberCategory(c)
	}
	return out
}

func fromNumberCategories(categories *[]NumberCategory) []string {
	if categories == nil {
		return nil
	}
	out := make([]string, len(*categories))
	for i, c := range *categories {
		out[i] = string(c)
	}
	return out
}

func toCallPermissionLevel(l trunk.CallPermissionLevel) CallPermissionLevel {
	return CallPermissionLevel{
		Id: l.ID, Name: l.Name, AllowedCategories: toNumberCategories(l.AllowedCategories),
		AbroadCountries:  append(AbroadCountries{}, l.AbroadCountries...),
		WithholdCallerId: l.WithholdCallerID, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt, Etag: trunk.ETag(l.Version),
	}
}

func (s *Server) ListCallPermissionLevels(ctx context.Context, req ListCallPermissionLevelsRequestObject) (ListCallPermissionLevelsResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(l trunk.CallPermissionLevel) uuid.UUID { return l.ID },
		func(before *uuid.UUID, limit int) ([]trunk.CallPermissionLevel, error) {
			return s.trunks.ListCallPermissionLevels(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListCallPermissionLevelsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := CallPermissionLevelList{Items: make([]CallPermissionLevel, 0, len(items)), NextCursor: next}
	for _, l := range items {
		list.Items = append(list.Items, toCallPermissionLevel(l))
	}
	return ListCallPermissionLevels200JSONResponse(list), nil
}

func (s *Server) CreateCallPermissionLevel(ctx context.Context, req CreateCallPermissionLevelRequestObject) (CreateCallPermissionLevelResponseObject, error) {
	l, err := s.trunks.CreateCallPermissionLevel(ctx, trunk.CallPermissionLevelInput{
		Name: req.Body.Name, AllowedCategories: fromNumberCategories(req.Body.AllowedCategories), WithholdCallerID: req.Body.WithholdCallerId,
		AbroadCountries: deref(req.Body.AbroadCountries),
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateCallPermissionLeveldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateCallPermissionLevel201JSONResponse(toCallPermissionLevel(l)), nil
}

func (s *Server) GetCallPermissionLevel(ctx context.Context, req GetCallPermissionLevelRequestObject) (GetCallPermissionLevelResponseObject, error) {
	l, err := s.trunks.GetCallPermissionLevel(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetCallPermissionLeveldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toCallPermissionLevel(l)
	return GetCallPermissionLevel200JSONResponse{Body: out, Headers: GetCallPermissionLevel200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateCallPermissionLevel(ctx context.Context, req UpdateCallPermissionLevelRequestObject) (UpdateCallPermissionLevelResponseObject, error) {
	l, err := s.trunks.UpdateCallPermissionLevel(ctx, req.Id, trunk.CallPermissionLevelPatch{
		Name: req.Body.Name, AllowedCategories: fromNumberCategories(req.Body.AllowedCategories), WithholdCallerID: req.Body.WithholdCallerId,
		AbroadCountries: req.Body.AbroadCountries,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateCallPermissionLeveldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toCallPermissionLevel(l)
	return UpdateCallPermissionLevel200JSONResponse{Body: out, Headers: UpdateCallPermissionLevel200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteCallPermissionLevel(ctx context.Context, req DeleteCallPermissionLevelRequestObject) (DeleteCallPermissionLevelResponseObject, error) {
	if err := s.trunks.DeleteCallPermissionLevel(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteCallPermissionLeveldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteCallPermissionLevel204Response{}, nil
}

// ListCountries is every country Linx can be set up in (ADR-085). The list
// never changes while the server runs, so it is built once.
func (s *Server) ListCountries(ctx context.Context, req ListCountriesRequestObject) (ListCountriesResponseObject, error) {
	return ListCountries200JSONResponse(countryList()), nil
}

var countryList = sync.OnceValue(func() CountryList {
	list := CountryList{Items: []Country{}}
	for _, c := range numbering.CountryList() {
		info, _ := numbering.CountryInfo(c.Region)
		out := Country{Code: c.Region, Name: c.Name, NationalPrefix: info.NationalPrefix}
		// Never null in the JSON, even for a country with no emergency number in the data.
		out.EmergencyNumbers = make([]struct {
			Label  string `json:"label"`
			Number string `json:"number"`
		}, 0, len(info.Emergency))
		for _, e := range info.Emergency {
			out.EmergencyNumbers = append(out.EmergencyNumbers, struct {
				Label  string `json:"label"`
				Number string `json:"number"`
			}{Label: e.Label, Number: e.Number})
		}
		list.Items = append(list.Items, out)
	}
	return list
})
