package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/backupschedule"
)

func toBackupSettings(s backupschedule.Schedule) BackupSettings {
	return BackupSettings{
		Frequency: BackupSettingsFrequency(s.Frequency), TimeOfDay: s.TimeOfDay,
		DayOfWeek: s.DayOfWeek, DayOfMonth: s.DayOfMonth, RequestedAt: s.RequestedAt,
	}
}

func (s *Server) GetBackupSettings(ctx context.Context, _ GetBackupSettingsRequestObject) (GetBackupSettingsResponseObject, error) {
	cur, err := s.backups.Get(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetBackupSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetBackupSettings200JSONResponse(toBackupSettings(cur)), nil
}

func (s *Server) UpdateBackupSettings(ctx context.Context, req UpdateBackupSettingsRequestObject) (UpdateBackupSettingsResponseObject, error) {
	patch := backupschedule.Patch{TimeOfDay: req.Body.TimeOfDay, DayOfWeek: req.Body.DayOfWeek, DayOfMonth: req.Body.DayOfMonth}
	if req.Body.Frequency != nil {
		v := string(*req.Body.Frequency)
		patch.Frequency = &v
	}
	out, err := s.backups.Update(ctx, patch)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateBackupSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return UpdateBackupSettings200JSONResponse(toBackupSettings(out)), nil
}

func toBackupRun(r backupschedule.Run) BackupRun {
	out := BackupRun{
		Id: r.ID, Trigger: BackupRunTrigger(r.Trigger), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Status: BackupRunStatus(r.Status), Destinations: make([]BackupRunDestination, 0, len(r.Destinations)),
	}
	if r.Error != "" {
		out.Error = &r.Error
	}
	for _, d := range r.Destinations {
		bd := BackupRunDestination{Name: d.Name, Ok: d.OK}
		if d.SnapshotID != "" {
			bd.SnapshotId = &d.SnapshotID
		}
		if d.Error != "" {
			bd.Error = &d.Error
		}
		out.Destinations = append(out.Destinations, bd)
	}
	return out
}

func (s *Server) ListBackups(ctx context.Context, req ListBackupsRequestObject) (ListBackupsResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(r backupschedule.Run) uuid.UUID { return r.ID },
		func(before *uuid.UUID, limit int) ([]backupschedule.Run, error) {
			return s.backups.History(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListBackupsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := BackupRunList{Items: make([]BackupRun, 0, len(items)), NextCursor: next}
	for _, r := range items {
		list.Items = append(list.Items, toBackupRun(r))
	}
	return ListBackups200JSONResponse(list), nil
}

func (s *Server) RequestBackup(ctx context.Context, _ RequestBackupRequestObject) (RequestBackupResponseObject, error) {
	if err := s.backups.RequestRun(ctx); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RequestBackupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RequestBackup202Response{}, nil
}
