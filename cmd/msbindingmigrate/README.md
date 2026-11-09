Internal `wsu.edu.kg` or `lclaitech.com` → `aishop6.com` auxiliary-mailbox migration. Preserves the existing auxiliary local part, credentials, sale switches and allocations. Does not export accounts. Uses the production Microsoft protocol, mailbox storage and proxy configuration.

Select `--source-domain lclaitech.com` for the newly authorized domain. It uses the separate ledger `microsoft_binding_migration_lclaitech_aishop6`; the default WSU ledger is retained. Each source requires its own state directory, bound by a `source-domain` marker. Never resume the stopped WSU container or reuse its checkpoints for the new source. Migration locks still prevent both sources from running concurrently.

For fixed samples, `--continue-on-error --apply --limit N --ids ...` finishes the listed accounts without letting one failed account halt the rest. It honors manual pause, leaves failures visible with their cooldowns, never selects another sample, and exits when the finite pass ends. Report incomplete sample audits separately; a zero process exit means the pass finished, not that every account succeeded.

Continuous runs check credential revisions at startup and between batches at least one hour after the preceding scan, queuing real mail rechecks for changed revisions in the selected ledger only.

Build and test:

```sh
go test ./cmd/msbindingmigrate ./cmd/msrebind ./internal/mailtransport/infra/msacl
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o msbindingmigrate ./cmd/msbindingmigrate
```

Run `deploy/msbindingmigrate/run.py` on the production host with the built binary and a private persistent state directory. Its default is a read-only preflight. `--apply --limit 1 --ids RESOURCE_ID` performs the first trial. Use disjoint groups of 5, 10, 50, 100 only after the previous group and its database audit pass. Omitting `--limit` and `--ids` processes the remaining scope. Concurrency is 10; claims are at most 1,000, reduced to the trial size.

The command creates a dedicated additive ledger table, `microsoft_binding_migration_wsu_aishop6`. Each claim atomically changes its accounts to `pending`, advances validation generations and records credential fingerprints. New proofs are verified before old proofs are removed. A fenced transaction writes the verified binding, records an operation log and moves the account to `mail_pending`, with resource status `abnormal`. The same worker then authorizes missing credentials and performs a real Graph/IMAP read. Only the atomic credential/readiness/audit commit marks the ledger `success` and the resource `normal`. Existing capability fields are preserved during rebinding; nonempty credentials alone do not prove that fetching mail works.

In trial mode, any failure stops dispatch and lets active accounts finish. In continuous mode, an account failure retains its failed/partial/mail_pending state and a persistent retry schedule while other accounts continue. Per the existing user policy, accounts actually bound to non-system mailboxes are excluded before mutation: their original status is restored and they are counted separately as skipped, never successful. An error after a possible remote mutation cannot become a skip. The original cleaner and maintenance queues must remain paused.

The user-authorized full rollout uses `--apply --skip-locked --continuous` without a limit or explicit IDs. `--skip-locked` also classifies previously confirmed, unchanged login-stage locks without another Microsoft request. Confirmed account locks become `skipped/account_locked`, retain their original binding and credentials, and remain abnormal. A lock discovered during mail verification after a fully committed replacement becomes `skipped/mail_account_locked`, retaining the verified replacement. MFA, phone checks, throttling and unknown failures are not treated as locks. Skips never count as completed mail verification.

On interruption, restart against the same state directory. Due claimed/partial items and `mail_pending` verification resume before new work; committed replacements are not repeated. Microsoft's flow reconciles existing proofs; private atomic checkpoint files retain sessions and remote completion evidence until commit. After investigating a before-change failure, `--retry-failed --ids RESOURCE_ID --limit 1` allows that exact account to retry. `docker stop` has a 960-second grace period for an in-flight bounded account operation.

Continuous mode records `retry_at` and `retry_count` in the ledger. Temporary failures wait 15 minutes, then 30 minutes, one hour, two hours, four hours and at most six hours. Password/MFA/phone/passkey/identity-fence failures wait 24 hours. A longer upstream Retry-After is always respected. Failures stay visible and never become successful merely because they were deferred. Accounts whose binding or owner changed before claim are retained for reconciliation without blocking the rest of the batch. Startup also queues a real mail recheck when a completed account's current credential revision has no matching receipt.

The continuous container uses Docker `unless-stopped`; runtime dependency failures reconnect after 30 seconds, and process/host restarts recover from the persistent ledger. When only future retries remain, the process waits instead of exiting. Manual pause persists in `state/PAUSED`; it stops new dispatch while active bounded operations finish, and survives process/host restarts. The deployment helper records the controlled container name in `state/continuous-container`. On the production host, with `control.py` beside the state directory:

```bash
sudo python3 control.py pause
sudo python3 control.py status
sudo python3 control.py resume
```

`resume` clears the persistent marker and also starts/unpauses a manually stopped container. Direct `docker stop` is respected by the `unless-stopped` policy; using `control.py pause` additionally preserves the pause across container replacement.

If inspection confirms a different current system mailbox and the user explicitly includes that account, `--confirmed-old-mailbox ADDRESS` accepts only one exact `--apply --ids RESOURCE_ID --limit 1` trial. It preserves the recorded target address and all database fences; Microsoft must still confirm the exact old proof before removing it. Repeat this argument when resuming that exception until it completes.

For an individually investigated account, `MSACL_FORCE_PASSWORD_LOGIN=1` in that container selects the existing password-plus-email verification flow. Start this separate attempt after the previous recovery-code lease expires; an unprocessed code must not trigger another channel in the same attempt.

After a committed migration, `--apply --verify-mail --ids RESOURCE_ID --limit 1` verifies that exact account through a real Graph/IMAP mailbox read. Missing OAuth credentials are acquired through ordinary authorization using the verified replacement mailbox. Refreshed credentials stay in private atomic checkpoints until a fenced transaction records the successful read, working credentials and `normal` status together. Existing passwords, bindings, sale switches and allocations are preserved. This explicitly authorized credential repair changes credential revisions and may change Graph availability; audit those changes against the separate `mailtransport.microsoft.binding_domain_mail_verify` operation log. A failed read cannot make an account normal or expand the migration.

Progress, with no credentials:

```sql
SELECT state, COUNT(*) FROM microsoft_binding_migration_wsu_aishop6 GROUP BY state;
SELECT resource_id, original_status, state, phase, attempts, retry_count, retry_at, safe_error
FROM microsoft_binding_migration_wsu_aishop6 WHERE state IN ('failed','partial','mail_pending','skipped');
```

Microsoft can require a security waiting period, passkey, phone verification or account recovery. The worker does not cancel pending security-info requests or label unresolved changes successful. All eligible trial accounts must succeed before rollout; report successful, skipped and failed counts against the original sample size. Success among eligible accounts does not mean 100% of the original inventory was migrated.
