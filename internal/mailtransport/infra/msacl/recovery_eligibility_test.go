package msacl

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateBindingRecoveryEligibility(t *testing.T) {
	base := PasswordRecoveryProbeResult{
		Proofs: []PasswordRecoveryProofInfo{{
			MaskedAddress: "qa*****@recovery.test",
			Type:          "Email",
			Channel:       "Email",
		}},
		MaskedBindingAddress: "qa*****@recovery.test",
		BindingAddress:       "qalpha01@recovery.test",
		BindingResolved:      true,
	}
	ready := RecoveryMailboxAccess{ReaderConfigured: true, ReaderReachable: true}
	domains := []string{" binding.example ", ".RECOVERY.test."}

	tests := []struct {
		name   string
		mutate func(*PasswordRecoveryProbeResult, *RecoveryMailboxAccess)
		want   BindingRecoveryEligibility
	}{
		{
			name: "unique project email proof and reachable reader",
			want: BindingRecoveryEligibility{Allowed: true},
		},
		{
			name: "local evidence is sufficient when reader health is unavailable",
			mutate: func(_ *PasswordRecoveryProbeResult, access *RecoveryMailboxAccess) {
				access.ReaderReachable = false
				access.LocalEvidence = true
			},
			want: BindingRecoveryEligibility{Allowed: true},
		},
		{
			name: "unresolved",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.BindingResolved = false
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipUnresolved},
		},
		{
			name: "ambiguous",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.BindingAmbiguous = true
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipAmbiguous},
		},
		{
			name: "multiple email proofs",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.Proofs = append(probe.Proofs, probe.Proofs[0])
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipEmailProofCount},
		},
		{
			name: "sms is not an email proof",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.Proofs[0].Channel = "SMS"
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipEmailProofCount},
		},
		{
			name: "proof mask mismatch",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.Proofs[0].MaskedAddress = "zz*****@recovery.test"
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipProofMismatch},
		},
		{
			name: "external mailbox",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.Proofs[0].MaskedAddress = "qa*****@external.test"
				probe.BindingAddress = "qalpha01@external.test"
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipExternalMailbox},
		},
		{
			name: "masked value is never a concrete recovered mailbox",
			mutate: func(probe *PasswordRecoveryProbeResult, _ *RecoveryMailboxAccess) {
				probe.BindingAddress = "qa*****@recovery.test"
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipUnresolved},
		},
		{
			name: "reader unavailable and no evidence",
			mutate: func(_ *PasswordRecoveryProbeResult, access *RecoveryMailboxAccess) {
				*access = RecoveryMailboxAccess{}
			},
			want: BindingRecoveryEligibility{Reason: BindingRecoverySkipMailboxUnreadable},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := base
			probe.Proofs = append([]PasswordRecoveryProofInfo(nil), base.Proofs...)
			access := ready
			if test.mutate != nil {
				test.mutate(&probe, &access)
			}
			require.Equal(t, test.want, EvaluateBindingRecoveryEligibility(probe, domains, access))
		})
	}
}

type recoveryAccessReader struct {
	emails []EmailObj
	err    error
	query  string
	limit  int
	fuzzy  bool
}

func (r *recoveryAccessReader) List(_ context.Context, mailbox string, limit int, fuzzy bool) ([]EmailObj, error) {
	r.query = mailbox
	r.limit = limit
	r.fuzzy = fuzzy
	return r.emails, r.err
}

func (*recoveryAccessReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, nil
}

func TestInspectRecoveryMailboxAccess(t *testing.T) {
	previous := activeMailboxReader()
	t.Cleanup(func() { SetMailboxReader(previous) })

	t.Run("reachable with exact historical evidence", func(t *testing.T) {
		reader := &recoveryAccessReader{emails: []EmailObj{{
			To: "Example User <QALPHA01@RECOVERY.test>",
		}}}
		SetMailboxReader(reader)

		access := InspectRecoveryMailboxAccess(context.Background(), " Qalpha01@RECOVERY.test ")

		require.Equal(t, RecoveryMailboxAccess{
			ReaderConfigured: true,
			ReaderReachable:  true,
			LocalEvidence:    true,
		}, access)
		require.Equal(t, "qalpha01@recovery.test", reader.query)
		require.Equal(t, 5, reader.limit)
		require.False(t, reader.fuzzy)
	})

	t.Run("reachable empty mailbox", func(t *testing.T) {
		SetMailboxReader(&recoveryAccessReader{})
		require.Equal(t, RecoveryMailboxAccess{
			ReaderConfigured: true,
			ReaderReachable:  true,
		}, InspectRecoveryMailboxAccess(context.Background(), "qalpha01@recovery.test"))
	})

	t.Run("reader error is not eligible evidence", func(t *testing.T) {
		SetMailboxReader(&recoveryAccessReader{err: errors.New("database unavailable")})
		require.Equal(t, RecoveryMailboxAccess{ReaderConfigured: true},
			InspectRecoveryMailboxAccess(context.Background(), "qalpha01@recovery.test"))
	})

	t.Run("reader not configured", func(t *testing.T) {
		SetMailboxReader(nil)
		require.Equal(t, RecoveryMailboxAccess{},
			InspectRecoveryMailboxAccess(context.Background(), "qalpha01@recovery.test"))
	})
}

func TestEvaluateActiveBindingRecoveryEligibilityRejectsReaderFailure(t *testing.T) {
	previousReader := activeMailboxReader()
	previousDomains := append([]string(nil), activeAuxiliaryDomains()...)
	t.Cleanup(func() {
		SetMailboxReader(previousReader)
		SetAuxiliaryDomains(previousDomains)
	})
	SetMailboxReader(&recoveryAccessReader{err: errors.New("database unavailable")})
	SetAuxiliaryDomains([]string{"recovery.test"})

	probe := PasswordRecoveryProbeResult{
		Proofs: []PasswordRecoveryProofInfo{{
			MaskedAddress: "qa*****@recovery.test",
			Type:          "Email",
			Channel:       "Email",
		}},
		BindingAddress:  "qalpha01@recovery.test",
		BindingResolved: true,
	}

	require.Equal(t,
		BindingRecoveryEligibility{Reason: BindingRecoverySkipMailboxUnreadable},
		EvaluateActiveBindingRecoveryEligibility(context.Background(), probe),
	)
}
