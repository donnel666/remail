// Command icloudliveness verifies selected database resources with the shared SMTP liveness worker.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/icloud"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

type options struct {
	from, until            time.Time
	limit                  int
	resourceID, operatorID uint
	apply, watch, recheck  bool
	timeout                time.Duration
}

type candidate struct {
	ID                   uint      `json:"id"`
	Version              uint64    `json:"version"`
	ValidationGeneration uint64    `json:"-"`
	ExpireAt             time.Time `json:"expireAt"`
	AliasCount           uint      `json:"aliasCount"`
	NewCookieStatus      string    `json:"newCookieStatus"`
	ResourceStatus       string    `json:"resourceStatus"`
}

type checkRun struct {
	ID                          uint64
	ResourceID                  uint
	Status                      string
	ProbeSender, ProbeRecipient string
	ProbeSentAt, NextCheckAt    *time.Time
	LastSafeError               string
	Attempts, MaxAttempts       int
}

type observation struct {
	Event          string     `json:"event"`
	ResourceID     uint       `json:"resourceId"`
	RunID          uint64     `json:"runId"`
	Status         string     `json:"status"`
	ResourceStatus string     `json:"resourceStatus"`
	Persisted      bool       `json:"persisted"`
	ProbeReceived  bool       `json:"probeReceived"`
	LastSafeError  string     `json:"lastSafeError,omitempty"`
	NextCheckAt    *time.Time `json:"nextCheckAt"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"maxAttempts"`
}

type diagnosticSender struct {
	mailapp.SenderPort
	stderr io.Writer
}

func (s diagnosticSender) Send(ctx context.Context, message maildomain.OutboundMessage) error {
	err := s.SenderPort.Send(ctx, message)
	if err == nil {
		fmt.Fprintln(s.stderr, "smtp_result accepted=true")
	} else {
		var failure *mailapp.OutboundSendFailure
		if errors.As(err, &failure) {
			fmt.Fprintf(s.stderr, "smtp_result recipient_rejected=%t retryable=%t detail=%q\n", failure.RecipientRejected, failure.Retryable, failure.Error())
		} else {
			fmt.Fprintln(s.stderr, "smtp_result accepted=false transport_error=true")
		}
	}
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "icloudliveness:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	var from, until string
	flags := flag.NewFlagSet("icloudliveness", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&from, "expire-from", "2026-11-08", "inclusive expiry date in Asia/Shanghai")
	flags.StringVar(&until, "expire-until", "2026-11-10", "exclusive expiry date in Asia/Shanghai")
	flags.IntVar(&opts.limit, "limit", 1, "number of matching resources (1-100)")
	flags.UintVar(&opts.resourceID, "resource-id", 0, "optionally require one specific matching resource")
	flags.BoolVar(&opts.recheck, "recheck", false, "include matching resources checked within the last 24 hours")
	flags.UintVar(&opts.operatorID, "operator-user-id", 1, "enabled admin or super-admin ID for the audit log")
	flags.BoolVar(&opts.apply, "apply", false, "send test emails and persist definite account health results")
	flags.BoolVar(&opts.watch, "watch", false, "continue checking late bounces until all observation windows finish")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "overall timeout; use 25h with -watch")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stderr)
			flags.PrintDefaults()
			return opts, flag.ErrHelp
		}
		return opts, errors.New("invalid command flags; use -help")
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	var err error
	opts.from, err = time.ParseInLocation(time.DateOnly, from, location)
	if err != nil {
		return opts, errors.New("invalid expiry start date")
	}
	opts.until, err = time.ParseInLocation(time.DateOnly, until, location)
	if err != nil {
		return opts, errors.New("invalid expiry end date")
	}
	if flags.NArg() != 0 || !opts.until.After(opts.from) || opts.limit < 1 || opts.limit > 100 || opts.timeout <= 0 ||
		(opts.resourceID != 0 && opts.limit != 1) || (opts.apply && opts.operatorID == 0) || (opts.watch && !opts.apply) {
		return opts, errors.New("invalid liveness selection or execution options")
	}
	return opts, nil
}

func selectCandidates(ctx context.Context, db *gorm.DB, opts options) ([]candidate, error) {
	query := db.WithContext(ctx).Table("icloud_resources AS ir").
		Select("ir.id, er.version, ir.validation_generation, ir.expire_at, ir.alias_count, ch.session_status AS new_cookie_status, ir.status AS resource_status").
		Joins("JOIN email_resources AS er ON er.id=ir.id AND er.type='icloud'").
		Joins("JOIN icloud_resource_channels AS ch ON ch.resource_id=ir.id AND ch.kind='apple_account'").
		Where("ir.status='normal' AND ir.alias_count>0 AND ir.alias_count<750 AND ch.session_status='invalid'").
		Where("ir.expire_at >= ? AND ir.expire_at < ?", opts.from.Format(time.DateTime), opts.until.Format(time.DateTime)).
		Where("EXISTS (SELECT 1 FROM icloud_aliases AS a WHERE a.resource_id=ir.id AND a.status='normal')").
		Order("ir.id ASC").Limit(opts.limit)
	if !opts.recheck {
		query = query.Where("NOT EXISTS (SELECT 1 FROM icloud_maintenance_runs AS r WHERE r.resource_id=ir.id AND r.kind IN ('liveness', 'liveness_smtp') AND r.queued_at >= ?)", time.Now().UTC().Add(-24*time.Hour))
	}
	if opts.resourceID != 0 {
		query = query.Where("ir.id=?", opts.resourceID)
	}
	var rows []candidate
	if err := query.Scan(&rows).Error; err != nil {
		return nil, errors.New("resource selection failed")
	}
	return rows, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	cfg, err := platform.Load()
	if err != nil {
		return errors.New("runtime configuration is unavailable")
	}
	cfg.MySQL.MaxOpenConns, cfg.MySQL.MaxIdleConns, cfg.Redis.PoolSize = 4, 2, 4
	p, cleanup, err := platform.New(ctx, cfg)
	if err != nil {
		return errors.New("database, Redis, or object storage is unavailable")
	}
	defer cleanup()
	rows, err := selectCandidates(ctx, p.DB, opts)
	if err != nil {
		return err
	}
	output := json.NewEncoder(stdout)
	if err := output.Encode(struct {
		Event      string      `json:"event"`
		Apply      bool        `json:"apply"`
		Candidates []candidate `json:"candidates"`
	}{"selection", opts.apply, rows}); err != nil {
		return err
	}
	if len(rows) != opts.limit {
		return errors.New("not enough matching resources")
	}
	if !opts.apply {
		return nil
	}
	for _, column := range []string{"probe_recipient", "probe_sender", "probe_sent_at", "next_check_at"} {
		if !p.DB.Migrator().HasColumn("icloud_maintenance_runs", column) {
			return errors.New("database migration 00146_icloud_smtp_liveness is required")
		}
	}
	var operator struct{ Role, Status string }
	if err := p.DB.WithContext(ctx).Table("users").Select("role, status").Where("id=?", opts.operatorID).Take(&operator).Error; err != nil || operator.Status != "active" || (operator.Role != "admin" && operator.Role != "super_admin") {
		return errors.New("operator must be an enabled admin or super-admin")
	}
	if !cfg.SMTP.InboundEnabled {
		return errors.New("inbound SMTP must be enabled")
	}
	settings, err := settingsinfra.NewRepository(p.DB).List(ctx)
	if err != nil {
		return errors.New("runtime settings are unavailable")
	}
	runtimeconfig.Replace(settings)
	files := governanceinfra.NewMinIOFileStore(p.MinIO, p.MinIOBucket)
	sender, err := mailinfra.NewConfiguredSMTPDelivery(cfg.SMTP)
	if err != nil {
		return errors.New("SMTP or DKIM configuration is unavailable")
	}
	inbox := mailapp.NewInboundService(nil, mailinfra.NewInboundResourceResolver(p.DB), files, nil, nil)
	service := icloud.NewService(p.DB, nil, files, p.Redis)
	from := cfg.SMTP.From
	if strings.TrimSpace(from) == "" {
		from = cfg.SMTP.Username
	}
	service.SetLivenessMail(diagnosticSender{SenderPort: sender, stderr: stderr}, inbox, from)
	service.SetLivenessReceiptStore(mailmatchapp.NewUseCase(mailmatchinfra.NewRepo(p.DB, files), nil, nil, nil))
	var runs []checkRun
	var startupErr error
	for _, row := range rows {
		key := "icloudliveness-" + platform.NewUUIDV7String()
		result, err := service.ApplyAdminICloudCommand(ctx, icloud.AdminICloudLiveness, row.ID, row.Version,
			opts.operatorID, key, key, "/cmd/icloudliveness")
		if err != nil {
			startupErr = errors.Join(startupErr, fmt.Errorf("resource %d could not start a liveness check: %w", row.ID, err))
			continue
		}
		if !result.Changed {
			startupErr = errors.Join(startupErr, fmt.Errorf("resource %d already has an active liveness check", row.ID))
			continue
		}
		var run checkRun
		if err := p.DB.WithContext(ctx).Table("icloud_maintenance_runs").
			Where("resource_id=? AND kind IN ('liveness', 'liveness_smtp') AND validation_generation=?", row.ID, row.ValidationGeneration+1).
			Take(&run).Error; err != nil {
			return errors.New("new liveness task could not be loaded")
		}
		runs = append(runs, run)
	}
	if startupErr != nil {
		fmt.Fprintln(stderr, startupErr)
	}
	return errors.Join(startupErr, monitor(ctx, service, p.DB, runs, opts.watch, output))
}

func monitor(ctx context.Context, service *icloud.Service, db *gorm.DB, runs []checkRun, watch bool, output *json.Encoder) error {
	lastStatus := make(map[uint64]string)
	lastRetry := make(map[uint64]int)
	var inconclusive bool
	for {
		active := false
		for i := range runs {
			run := &runs[i]
			if lastStatus[run.ID] != "" && (!watch || run.NextCheckAt == nil) {
				continue
			}
			if run.NextCheckAt != nil && run.NextCheckAt.After(time.Now()) {
				active = true
				continue
			}
			if err := service.ProcessICloudLivenessCheck(ctx, run.ID); err != nil {
				fmt.Fprintf(os.Stderr, "resource %d: bounce inspection will retry if its deadline permits\n", run.ResourceID)
			}
			var fresh checkRun
			if err := db.WithContext(ctx).Table("icloud_maintenance_runs").Where("id=?", run.ID).Take(&fresh).Error; err != nil {
				return errors.New("liveness task state is unavailable")
			}
			*run = fresh
			if run.Status == "queued" && run.Attempts > lastRetry[run.ID] {
				if err := output.Encode(observation{Event: "alias_retry", ResourceID: run.ResourceID, RunID: run.ID,
					Status: run.Status, LastSafeError: run.LastSafeError, NextCheckAt: run.NextCheckAt,
					Attempts: run.Attempts, MaxAttempts: run.MaxAttempts}); err != nil {
					return err
				}
				lastRetry[run.ID] = run.Attempts
			}
			if run.Status == "queued" || run.Status == "running" {
				active = true
				continue
			}
			if watch && run.NextCheckAt != nil {
				active = true
			}
			if lastStatus[run.ID] == run.Status {
				continue
			}
			var resource struct{ Status string }
			if err := db.WithContext(ctx).Table("icloud_resources").Select("status").Where("id=?", run.ResourceID).Take(&resource).Error; err != nil {
				return errors.New("persisted resource state is unavailable")
			}
			result := observation{Event: "result", ResourceID: run.ResourceID, RunID: run.ID, Status: run.Status,
				ResourceStatus: resource.Status, LastSafeError: run.LastSafeError, NextCheckAt: run.NextCheckAt,
				Attempts: run.Attempts, MaxAttempts: run.MaxAttempts}
			result.Persisted = run.Status == "succeeded" && resource.Status == "normal" ||
				run.Status == "failed" && resource.Status == "abnormal" && strings.Contains(run.LastSafeError, "iCloud alias")
			result.ProbeReceived = run.Status == "succeeded" && run.ProbeSentAt == nil
			if run.Status == "succeeded" && run.ProbeSentAt != nil {
				fetched, err := service.FetchMail(ctx, icloud.MailFetchRequest{
					ResourceID: run.ResourceID, Recipient: run.ProbeRecipient, SinceAt: *run.ProbeSentAt, MaxMessages: 30,
				})
				if err == nil && fetched != nil {
					for _, message := range fetched.Messages {
						if strings.EqualFold(message.Sender, run.ProbeSender) {
							result.ProbeReceived = true
							break
						}
					}
				}
			}
			if !result.Persisted {
				inconclusive = true
			}
			if err := output.Encode(result); err != nil {
				return err
			}
			lastStatus[run.ID] = run.Status
		}
		if !active {
			if inconclusive {
				return errors.New("one or more checks ended without a definite account health result")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("liveness observation stopped; unfinished tasks remain persisted")
		case <-time.After(10 * time.Second):
		}
	}
}
