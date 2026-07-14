package msacl

import (
	"context"
	"net/mail"
	"strings"
)

// BindingRecoverySkipReason is a safe, non-secret explanation for why the
// validation path must not try to repair a Microsoft recovery-mailbox binding.
type BindingRecoverySkipReason string

const (
	BindingRecoverySkipUnresolved        BindingRecoverySkipReason = "unresolved"
	BindingRecoverySkipAmbiguous         BindingRecoverySkipReason = "ambiguous"
	BindingRecoverySkipEmailProofCount   BindingRecoverySkipReason = "email_proof_count"
	BindingRecoverySkipProofMismatch     BindingRecoverySkipReason = "proof_mismatch"
	BindingRecoverySkipExternalMailbox   BindingRecoverySkipReason = "external_mailbox"
	BindingRecoverySkipMailboxUnreadable BindingRecoverySkipReason = "mailbox_unreadable"
)

// RecoveryMailboxAccess describes the non-destructive evidence available for
// a project-owned recovery mailbox. A successful exact List call establishes
// that the local receiving store can be queried. LocalEvidence is stronger: a
// message addressed to this exact mailbox already exists in that store.
type RecoveryMailboxAccess struct {
	ReaderConfigured bool
	ReaderReachable  bool
	LocalEvidence    bool
}

// BindingRecoveryEligibility is deliberately small and safe to log. It never
// contains a full mailbox, Microsoft proof id, recovery token, or OTP.
type BindingRecoveryEligibility struct {
	Allowed bool
	Reason  BindingRecoverySkipReason
}

// InspectRecoveryMailboxAccess checks the injected local MailboxReader without
// sending a message. The exact, non-fuzzy query is the same read path used by
// the OTP watcher. An empty successful result still proves reader reachability;
// it does not manufacture historical evidence.
func InspectRecoveryMailboxAccess(ctx context.Context, mailbox string) RecoveryMailboxAccess {
	reader := activeMailboxReader()
	access := RecoveryMailboxAccess{ReaderConfigured: reader != nil}
	if reader == nil || ctx == nil || normalizeRecoveryMailbox(mailbox) == "" {
		return access
	}

	emails, err := reader.List(ctx, normalizeRecoveryMailbox(mailbox), 5, false)
	if err != nil {
		return access
	}
	access.ReaderReachable = true
	for _, email := range emails {
		if recoveryMailboxEvidenceMatches(mailbox, email.To) {
			access.LocalEvidence = true
			break
		}
	}
	return access
}

// EvaluateBindingRecoveryEligibility is the pure policy boundary used before
// validation may attempt to confirm a recovered binding candidate. It does not
// make the candidate a verified fact; callers must still complete the normal
// password/OTP login against that exact address before persisting verified.
// Recovery confirmation is allowed only when:
//   - Microsoft exposes exactly one usable Email proof;
//   - that proof was uniquely resolved and matches the recovered address;
//   - the address belongs to a configured, normal binding domain; and
//   - the local mailbox reader is reachable or independent local evidence is
//     already available.
//
// The last condition intentionally rejects an address resolved solely from a
// preferred value or deterministic naming rule when the receiving path cannot
// be queried.
func EvaluateBindingRecoveryEligibility(
	probe PasswordRecoveryProbeResult,
	bindingDomains []string,
	access RecoveryMailboxAccess,
) BindingRecoveryEligibility {
	if probe.BindingAmbiguous {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipAmbiguous}
	}
	address := normalizeRecoveryMailbox(probe.BindingAddress)
	if !probe.BindingResolved || address == "" {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipUnresolved}
	}

	emailProofs := make([]PasswordRecoveryProofInfo, 0, 1)
	for _, proof := range probe.Proofs {
		if isPasswordRecoveryEmailProofInfo(proof) {
			emailProofs = append(emailProofs, proof)
		}
	}
	if len(emailProofs) != 1 {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipEmailProofCount}
	}
	if !mailboxMatchesMasked(emailProofs[0].MaskedAddress, address) {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipProofMismatch}
	}
	if !recoveryMailboxUsesBindingDomain(address, bindingDomains) {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipExternalMailbox}
	}
	if !access.ReaderReachable && !access.LocalEvidence {
		return BindingRecoveryEligibility{Reason: BindingRecoverySkipMailboxUnreadable}
	}
	return BindingRecoveryEligibility{Allowed: true}
}

// EvaluateActiveBindingRecoveryEligibility applies the policy using the
// process-wide mailbox reader and binding domains loaded by the service at
// startup. Reader failures are represented as a skip decision, so this
// best-effort safeguard never turns a normal validation into an outage.
func EvaluateActiveBindingRecoveryEligibility(ctx context.Context, probe PasswordRecoveryProbeResult) BindingRecoveryEligibility {
	access := InspectRecoveryMailboxAccess(ctx, probe.BindingAddress)
	return EvaluateBindingRecoveryEligibility(probe, activeAuxiliaryDomains(), access)
}

func isPasswordRecoveryEmailProofInfo(proof PasswordRecoveryProofInfo) bool {
	return strings.EqualFold(strings.TrimSpace(proof.Type), "Email") &&
		strings.EqualFold(firstNonEmpty(strings.TrimSpace(proof.Channel), "Email"), "Email") &&
		strings.Contains(strings.TrimSpace(proof.MaskedAddress), "@")
}

func recoveryMailboxUsesBindingDomain(address string, bindingDomains []string) bool {
	address = normalizeRecoveryMailbox(address)
	_, domain, ok := strings.Cut(address, "@")
	if !ok {
		return false
	}
	for _, candidate := range bindingDomains {
		candidate = strings.Trim(strings.ToLower(strings.TrimSpace(candidate)), ".")
		if candidate != "" && domain == candidate {
			return true
		}
	}
	return false
}

func normalizeRecoveryMailbox(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.Contains(value, "*") || strings.ContainsAny(value, "\r\n\t ") {
		return ""
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), value) {
		return ""
	}
	local, domain, ok := strings.Cut(parsed.Address, "@")
	domain = strings.Trim(domain, ".")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return ""
	}
	return local + "@" + domain
}

func recoveryMailboxEvidenceMatches(mailbox, recipient string) bool {
	want := normalizeRecoveryMailbox(mailbox)
	if want == "" {
		return false
	}
	if normalizeRecoveryMailbox(recipient) == want {
		return true
	}
	addresses, err := mail.ParseAddressList(recipient)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if normalizeRecoveryMailbox(address.Address) == want {
			return true
		}
	}
	return false
}
