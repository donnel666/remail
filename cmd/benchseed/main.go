package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	benchSuperAdminID = int64(900_000_000)
	benchUserID       = int64(900_000_001)
	benchProjectID    = int64(900_000_001)
	benchProductID    = int64(900_000_001)
	benchDebitID      = int64(900_000_001)
	resourceIDBase    = int64(1_000_000_000)
	aliasIDBase       = int64(2_000_000_000)
	allocationIDBase  = int64(3_000_000_000)
	orderIDBase       = int64(4_000_000_000)
	eventIDBase       = int64(5_000_000_000)
	messageIDBase     = int64(6_000_000_000)
	walletTxIDBase    = int64(7_000_000_000)
	tokenIDBase       = int64(8_000_000_000)
)

func main() {
	var (
		dsn       = flag.String("dsn", os.Getenv("MYSQL_DSN"), "MySQL DSN")
		profile   = flag.String("profile", "resources", "resources|aliases|orders|messages")
		count     = flag.Int64("count", 1_000_000, "rows to create")
		resources = flag.Int64("resources", 1_000_000, "existing Microsoft resource count")
		orders    = flag.Int64("orders", 10_000_000, "existing benchmark order count")
		batchSize = flag.Int("batch", 1000, "rows per transaction")
	)
	flag.Parse()
	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("-dsn or MYSQL_DSN is required")
	}
	if *count < 0 || *resources <= 0 || *orders <= 0 || *batchSize <= 0 {
		log.Fatal("counts and batch size must be positive")
	}
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	started := time.Now()
	switch *profile {
	case "resources":
		must(seedFoundation(ctx, db))
		must(seedResources(ctx, db, *count, *batchSize))
	case "aliases":
		must(seedFoundation(ctx, db))
		must(seedAliases(ctx, db, *count, *resources, *batchSize))
	case "orders":
		must(seedFoundation(ctx, db))
		must(seedOrders(ctx, db, *count, *resources, *batchSize))
	case "messages":
		must(seedMessages(ctx, db, *count, *resources, *orders, *batchSize))
	default:
		log.Fatalf("unsupported profile %q", *profile)
	}
	log.Printf("profile=%s rows=%d elapsed=%s", *profile, *count, time.Since(started).Round(time.Millisecond))
}

func seedFoundation(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`INSERT IGNORE INTO users(id,email,password_hash,nickname,enabled,role) VALUES
			 (900000000,'bench-super-admin@remail.local','bench','bench-super-admin',TRUE,'super_admin'),
			 (900000001,'bench@remail.local','bench','bench',TRUE,'supplier')`,
		`INSERT IGNORE INTO projects(id,name,target_platform,status,access_type,loose_match)
		 VALUES (900000001,'Benchmark Project','benchmark','listed','public',TRUE)`,
		`INSERT IGNORE INTO project_products(
			id,project_id,type,status,code_enabled,purchase_enabled,
			code_price,purchase_price,code_supplier_price,purchase_supplier_price,
			code_window_minutes,activation_window_minutes,warranty_minutes,
			main_weight,dot_weight,plus_weight
		 ) VALUES (900000001,900000001,'microsoft','enabled',TRUE,TRUE,0,0,0,0,10,60,1440,1,0,0)`,
		`INSERT IGNORE INTO wallets(user_id,consumer_balance,supplier_available,supplier_frozen)
		 VALUES (900000001,0,0,0)`,
		`INSERT IGNORE INTO wallet_transactions(
			id,transaction_no,user_id,transaction_type,balance_bucket,direction,amount,
			balance_before,balance_after,biz_type,biz_id,idempotency_key
		 ) VALUES (900000001,'WT_BENCH_ZERO',900000001,'debit','consumer','out',0,0,0,'benchmark','foundation','bench-zero')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func seedResources(ctx context.Context, db *sql.DB, count int64, batchSize int) error {
	return forBatches(count, int64(batchSize), func(start, end int64) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := insertRows(ctx, tx,
			"INSERT IGNORE INTO email_resources(id,type,owner_user_id) VALUES ",
			start, end, func(i int64) (string, []any) {
				return "(?,'microsoft',?)", []any{resourceIDBase + i, benchUserID}
			}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertRows(ctx, tx,
			`INSERT IGNORE INTO microsoft_resources(
				id,resource_type,email_address,email_domain,password,client_id,refresh_token,
				for_sale,status,quality_score,alloc_bucket
			) VALUES `,
			start, end, func(i int64) (string, []any) {
				id := resourceIDBase + i
				return "(?,'microsoft',?,'bench.local','bench','','',TRUE,'normal',100,?)",
					[]any{id, fmt.Sprintf("ms-%d@bench.local", i), id % 64}
			}); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

func seedAliases(ctx context.Context, db *sql.DB, count, resources int64, batchSize int) error {
	return forBatches(count, int64(batchSize), func(start, end int64) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = insertRows(ctx, tx,
			"INSERT IGNORE INTO explicit_aliases(id,resource_id,owner_user_id,email,status) VALUES ",
			start, end, func(i int64) (string, []any) {
				resourceID := resourceIDBase + i%resources
				return "(?,?,?,?,'normal')", []any{aliasIDBase + i, resourceID, benchSuperAdminID, fmt.Sprintf("alias-%d@bench.local", i)}
			})
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

func seedOrders(ctx context.Context, db *sql.DB, count, resources int64, batchSize int) error {
	activeCount := min(count, resources, 200_000)
	return forBatches(count, int64(batchSize), func(start, end int64) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := insertRows(ctx, tx, "INSERT IGNORE INTO allocation_order_guards(order_no,type) VALUES ",
			start, end, func(i int64) (string, []any) {
				return "(?,'microsoft')", []any{benchOrderNo(i)}
			}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertRows(ctx, tx, `INSERT IGNORE INTO microsoft_allocations(
			id,order_no,guard_type,project_id,product_id,resource_id,supply_scope,mailbox,email,status,released_at
		) VALUES `, start, end, func(i int64) (string, []any) {
			resourceIndex := i % resources
			status := "released"
			var releasedAt any = time.Now().UTC()
			if i < activeCount {
				status = "allocated"
				releasedAt = nil
			}
			return "(?,?,'microsoft',?,?,?,'public','main',?,?,?)", []any{
				allocationIDBase + i, benchOrderNo(i), benchProjectID, benchProductID,
				resourceIDBase + resourceIndex, fmt.Sprintf("ms-%d@bench.local", resourceIndex),
				status, releasedAt,
			}
		}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertRows(ctx, tx, `INSERT IGNORE INTO wallet_transactions(
			id,transaction_no,user_id,transaction_type,balance_bucket,direction,amount,
			balance_before,balance_after,biz_type,biz_id,idempotency_key
		) VALUES `, start, end, func(i int64) (string, []any) {
			return "(?,? ,?,'debit','consumer','out',0,0,0,'order',?,?)", []any{
				walletTxIDBase + i,
				fmt.Sprintf("WT_BENCH_%012d", i),
				benchUserID,
				benchOrderNo(i),
				fmt.Sprintf("bench-debit-%d", i),
			}
		}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertRows(ctx, tx, `INSERT IGNORE INTO orders(
			id,order_no,user_id,project_id,project_product_id,product_type,service_mode,supply_policy,
			status,failure_code,pay_amount,refund_amount,debit_tx_id,allocation_type,microsoft_alloc_id,
			delivery_email,receive_started_at,receive_until,after_sale_until,client_channel,
			idempotency_key,request_fingerprint,service_cleanup_status
		) VALUES `, start, end, func(i int64) (string, []any) {
			resourceIndex := i % resources
			serviceMode := "purchase"
			status := "completed"
			receiveUntil := time.Now().UTC()
			afterSaleUntil := receiveUntil
			if i < activeCount {
				serviceMode = "code"
				status = "active"
				receiveUntil = time.Now().UTC().Add(time.Hour)
				if i%10 == 0 {
					receiveUntil = time.Now().UTC().Add(-time.Minute)
				}
				afterSaleUntil = receiveUntil
			}
			return "(?,?,?, ?,?,'microsoft',?,'public_only',?,'',0,0,?,'microsoft',?,?,NOW(),?,?,'console',?,?,'none')", []any{
				orderIDBase + i, benchOrderNo(i), benchUserID, benchProjectID, benchProductID,
				serviceMode, status, walletTxIDBase + i,
				allocationIDBase + i, fmt.Sprintf("ms-%d@bench.local", resourceIndex),
				receiveUntil, afterSaleUntil,
				fmt.Sprintf("bench-order-%d", i), hashHex(fmt.Sprintf("bench-order-%d", i)),
			}
		}); err != nil {
			_ = tx.Rollback()
			return err
		}
		activeEnd := end
		if activeEnd > activeCount {
			activeEnd = activeCount
		}
		if start < activeEnd {
			if err := insertRows(ctx, tx, `INSERT IGNORE INTO order_tokens(
				id,token_prefix,token_plain,order_no,enabled
			) VALUES `, start, activeEnd, func(i int64) (string, []any) {
				return "(?,?,?,?,TRUE)", []any{
					tokenIDBase + i,
					fmt.Sprintf("st%012d", i),
					fmt.Sprintf("st_bench_%012d", i),
					benchOrderNo(i),
				}
			}); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		eventSpecs := []struct {
			eventType  string
			fromStatus any
			toStatus   string
		}{
			{eventType: "order.created", fromStatus: nil, toStatus: "pending_payment"},
			{eventType: "order.paid", fromStatus: "pending_payment", toStatus: "paid"},
			{eventType: "order.service_started", fromStatus: "paid", toStatus: ""},
		}
		for eventOffset, spec := range eventSpecs {
			if err := insertRows(ctx, tx, `INSERT IGNORE INTO order_events(
			id,event_no,order_no,event_type,from_status,to_status,operator_type,reason
		) VALUES `, start, end, func(i int64) (string, []any) {
				eventType := spec.eventType
				toStatus := spec.toStatus
				if eventOffset == 2 {
					if i < activeCount {
						eventType = "order.active"
						toStatus = "active"
					} else {
						eventType = "order.completed"
						toStatus = "completed"
					}
				}
				return "(?,?,?,?,?,?,'system','benchmark')", []any{
					eventIDBase + i*3 + int64(eventOffset),
					fmt.Sprintf("OE_BENCH_%d_%d", i, eventOffset),
					benchOrderNo(i),
					eventType,
					spec.fromStatus,
					toStatus,
				}
			}); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		return tx.Commit()
	})
}

func seedMessages(ctx context.Context, db *sql.DB, count, resources, orders int64, batchSize int) error {
	return forBatches(count, int64(batchSize), func(start, end int64) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = insertRows(ctx, tx, `INSERT IGNORE INTO mailmatch_messages(
			id,email_resource_id,resource_type,matched_order_id,recipient,sender,subject,raw_body,
			body_preview,verification_code,dedupe_key,protocol,folder,status,received_at
		) VALUES `, start, end, func(i int64) (string, []any) {
			resourceIndex := i % resources
			orderIndex := i % orders
			code := fmt.Sprintf("%06d", i%1_000_000)
			body := "Benchmark verification code " + code
			receivedAt := time.Now().UTC().Add(-time.Duration(i%120) * time.Hour)
			return "(?,?,'microsoft',?,?,?,?,?,?,?,?,?,'inbox','matched',?)", []any{
				messageIDBase + i, resourceIDBase + resourceIndex, orderIDBase + orderIndex,
				fmt.Sprintf("ms-%d@bench.local", resourceIndex), "noreply@bench.local",
				"Benchmark verification", body, body, code, hashHex(fmt.Sprintf("message-%d", i)), "graph",
				receivedAt,
			}
		})
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

func insertRows(
	ctx context.Context,
	tx *sql.Tx,
	prefix string,
	start, end int64,
	row func(int64) (string, []any),
) error {
	var query strings.Builder
	query.WriteString(prefix)
	args := make([]any, 0, (end-start)*4)
	for i := start; i < end; i++ {
		if i > start {
			query.WriteByte(',')
		}
		sqlRow, values := row(i)
		query.WriteString(sqlRow)
		args = append(args, values...)
	}
	_, err := tx.ExecContext(ctx, query.String(), args...)
	return err
}

func forBatches(total, batchSize int64, run func(start, end int64) error) error {
	for start := int64(0); start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		if err := run(start, end); err != nil {
			return fmt.Errorf("batch %d-%d: %w", start, end, err)
		}
		if end%100_000 == 0 || end == total {
			log.Printf("seeded %d/%d", end, total)
		}
	}
	return nil
}

func benchOrderNo(i int64) string {
	return fmt.Sprintf("OR_BENCH_%012d", i)
}

func hashHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
