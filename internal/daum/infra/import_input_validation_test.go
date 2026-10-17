package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/daum/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDaumOversizedGeneratedAliasIsALineError(t *testing.T) {
	for _, strategy := range []string{domain.ErrorStrategySkip, domain.ErrorStrategyAbort} {
		t.Run(strategy, func(t *testing.T) {
			s, files := newDaumAsyncTestService(t)
			ctx := context.Background()
			// SQLite ignores VARCHAR widths; enforce the actual migration's limit.
			require.NoError(t, s.DB.Exec("CREATE TRIGGER alias_width BEFORE INSERT ON daum_explicit_aliases WHEN length(NEW.email_address) > 255 BEGIN SELECT RAISE(ABORT, 'Data too long for column email_address'); END").Error)
			longEmail := strings.Repeat("a", 246) + "@daum.net"
			content := []byte("first@daum.net----synthetic-first\n" + longEmail + "----synthetic-long\nlast@daum.net----synthetic-last")
			id, _, err := s.CreateImportWithArtifact(ctx, 7, 7, strategy, "alias-width", "source", "fixture.txt", "", content)
			require.NoError(t, err)
			files.objects["source"] = content
			_, err = s.ProcessImportTask(ctx, id, 1)
			require.NoError(t, err)
			status, err := s.GetImport(ctx, id)
			require.NoError(t, err)
			require.EqualValues(t, 1, status.Generation, "bad input must not be scheduled as an infrastructure retry")
			var resources int64
			require.NoError(t, s.DB.Model(&Resource{}).Count(&resources).Error)
			if strategy == domain.ErrorStrategySkip {
				require.Equal(t, domain.ImportImported, status.Status)
				require.Equal(t, "succeeded", status.DispatchStatus)
				require.EqualValues(t, 2, resources)
				require.Equal(t, 2, status.AcceptedCount)
				require.Equal(t, 2, status.ImportedCount)
				require.Equal(t, 1, status.SkippedCount)
			} else {
				require.Equal(t, domain.ImportFailed, status.Status)
				require.Equal(t, "failed", status.DispatchStatus)
				require.Zero(t, resources, "abort must reject the entire input before creating its first valid row")
				require.Zero(t, status.ImportedCount)
				require.Equal(t, 1, status.FailedCount)
			}
			page, err := s.ListImportItems(ctx, id, 0, 20)
			require.NoError(t, err)
			var invalid *ImportItemStatus
			for index := range page.Items {
				if page.Items[index].LineNumber == 2 {
					invalid = &page.Items[index]
				}
			}
			require.NotNil(t, invalid)
			require.Equal(t, "invalid_format", invalid.Category)
			require.Nil(t, invalid.ResourceID)
			require.NotEmpty(t, status.FailureObjectKey)
			require.NotContains(t, string(files.objects[status.FailureObjectKey]), "synthetic-")
		})
	}
}

func TestDaumAliasLengthGuardAlsoProtectsDirectImportAndAccountCorrection(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	tooLong := strings.Repeat("a", 246) + "@daum.net"
	_, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: tooLong, AppPassword: "synthetic"})
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "kept@daum.net", AppPassword: "synthetic"})
	require.NoError(t, err)
	before, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Version: before.Version, Action: "edit", Email: &tooLong, OperatorUserID: 7, IdempotencyKey: "oversized-correction"})
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	after, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, before.EmailAddress, after.EmailAddress)
	require.Equal(t, before.Version, after.Version)
	aliases, err := s.ListAliases(ctx, id)
	require.NoError(t, err)
	require.Len(t, aliases, 2)
	require.Equal(t, "kept@hanmail.net", aliases[0].EmailAddress)
	var resources int64
	require.NoError(t, s.DB.Model(&Resource{}).Count(&resources).Error)
	require.EqualValues(t, 1, resources)
}

func TestDaumImportDeterministicDatabaseAndInputErrorsBecomeSafeFailures(t *testing.T) {
	for name, injected := range map[string]error{
		"data_too_long":  fmt.Errorf("database operation: %w", &mysql.MySQLError{Number: 1406, Message: "private-fixture-value must not reach diagnostics"}),
		"invalid_input":  domain.ErrInvalidResource,
		"invalid_format": domain.ErrInvalidImportFormat,
	} {
		t.Run(name, func(t *testing.T) {
			s, files := newDaumAsyncTestService(t)
			ctx := context.Background()
			calls := 0
			require.NoError(t, s.DB.Callback().Create().Before("gorm:create").Register("review_input_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "daum_explicit_aliases" {
					calls++
					_ = tx.AddError(injected)
				}
			}))
			content := []byte("one@daum.net----synthetic-app")
			id, _, err := s.CreateImportWithArtifact(ctx, 7, 7, domain.ErrorStrategySkip, "input-error", "source", "fixture.txt", "", content)
			require.NoError(t, err)
			files.objects["source"] = content
			_, err = s.ProcessImportTask(ctx, id, 1)
			require.NoError(t, err)
			status, err := s.GetImport(ctx, id)
			require.NoError(t, err)
			require.Equal(t, domain.ImportFailed, status.Status)
			require.Equal(t, "failed", status.DispatchStatus)
			require.EqualValues(t, 1, status.Generation)
			require.Equal(t, 1, status.FailedCount)
			require.NotEmpty(t, status.FailureObjectKey)
			diagnostics := string(files.objects[status.FailureObjectKey])
			require.Contains(t, diagnostics, "1,one@daum.net,")
			require.NotContains(t, diagnostics, "private-fixture-value")
			require.NotContains(t, diagnostics, "synthetic-app")
			var resources int64
			require.NoError(t, s.DB.Model(&Resource{}).Count(&resources).Error)
			require.Zero(t, resources, "the failed chunk must roll back its earlier root and resource inserts")
			_, err = s.ProcessImportTask(ctx, id, 1)
			require.NoError(t, err)
			require.Equal(t, 1, calls, "terminal input failures must not execute the row again")
		})
	}
}

func TestDaumImportTransientDatabaseErrorsStillResume(t *testing.T) {
	for name, injected := range map[string]error{
		"deadlock":        &mysql.MySQLError{Number: 1213, Message: "synthetic deadlock"},
		"lock_wait":       &mysql.MySQLError{Number: 1205, Message: "synthetic lock wait timeout"},
		"connection":      &mysql.MySQLError{Number: 2006, Message: "synthetic connection loss"},
		"unknown_error":   errors.New("synthetic data access failure"),
		"untyped_message": errors.New("Data too long is not enough to classify an untyped error"),
	} {
		t.Run(name, func(t *testing.T) {
			s, files := newDaumAsyncTestService(t)
			ctx := context.Background()
			fail := true
			require.NoError(t, s.DB.Callback().Create().Before("gorm:create").Register("review_transient_failure", func(tx *gorm.DB) {
				if fail && tx.Statement.Table == "daum_explicit_aliases" {
					_ = tx.AddError(injected)
				}
			}))
			content := []byte("one@daum.net----synthetic-app")
			id, _, err := s.CreateImportWithArtifact(ctx, 7, 7, domain.ErrorStrategySkip, "transient-error", "source", "fixture.txt", "", content)
			require.NoError(t, err)
			files.objects["source"] = content
			_, err = s.ProcessImportTask(ctx, id, 1)
			require.ErrorIs(t, err, injected)
			status, err := s.GetImport(ctx, id)
			require.NoError(t, err)
			require.Equal(t, domain.ImportProcessing, status.Status)
			require.Equal(t, "pending", status.DispatchStatus)
			require.EqualValues(t, 2, status.Generation)
			require.Empty(t, status.FailureObjectKey)
			fail = false
			_, err = s.ProcessImportTask(ctx, id, 2)
			require.NoError(t, err)
			status, err = s.GetImport(ctx, id)
			require.NoError(t, err)
			require.Equal(t, domain.ImportImported, status.Status)
			require.Equal(t, 1, status.ImportedCount)
		})
	}
}

type unavailableDaumFailureReports struct{ *daumMemoryFiles }

func (unavailableDaumFailureReports) SavePrivate(context.Context, governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	return nil, errors.New("synthetic private storage outage")
}

func TestDaumDeterministicImportFailureRetriesUnavailableDiagnostics(t *testing.T) {
	s, files := newDaumAsyncTestService(t)
	ctx := context.Background()
	require.NoError(t, s.DB.Callback().Create().Before("gorm:create").Register("review_width_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "daum_explicit_aliases" {
			_ = tx.AddError(&mysql.MySQLError{Number: 1406, Message: "synthetic width error"})
		}
	}))
	content := []byte("one@daum.net----synthetic-app")
	id, _, err := s.CreateImportWithArtifact(ctx, 7, 7, domain.ErrorStrategySkip, "diagnostics-outage", "source", "fixture.txt", "", content)
	require.NoError(t, err)
	files.objects["source"] = content
	s.Files = unavailableDaumFailureReports{files}
	_, err = s.ProcessImportTask(ctx, id, 1)
	require.Error(t, err)
	status, err := s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.ImportProcessing, status.Status)
	require.Equal(t, "pending", status.DispatchStatus)
	require.EqualValues(t, 2, status.Generation)
	s.Files = files
	_, err = s.ProcessImportTask(ctx, id, 2)
	require.NoError(t, err)
	status, err = s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.ImportFailed, status.Status)
	require.Equal(t, "failed", status.DispatchStatus)
	require.EqualValues(t, 2, status.Generation)
	require.NotEmpty(t, status.FailureObjectKey)
}
