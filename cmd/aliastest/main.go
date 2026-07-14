// Command aliastest is a standalone harness to iterate on the Microsoft
// explicit-alias OTC flow WITHOUT rebuilding/redeploying the whole app image.
//
// It injects a lightweight MailboxReader (direct MySQL inbound_mails + MinIO
// .eml) and calls msacl.SyncAndAddExplicitAliases, so you can run one real
// account end-to-end on the test server and see every OTC step + dump the
// passkey/interrupt page for offline analysis.
//
// Build (from repo root):
//
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/aliastest ./cmd/aliastest
//
// Run on the server (MySQL/MinIO reached via container IPs):
//
//	MSACL_DEBUG_LOGS=1 /tmp/aliastest \
//	  -email a@outlook.com -binding ocom_x@aishop6.com \
//	  -proxy 'socks5://u:p@host:port' \
//	  -mysql 'remail:PWD@tcp(172.18.0.9:3306)/remail' \
//	  -minio 172.18.0.2:9000 -minio-key remail -minio-secret PWD -bucket remail \
//	  -candidates ''   # empty = only login+list (still sends 1 OTP)
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
)

type dbReader struct {
	db     *sql.DB
	mc     *minio.Client
	bucket string
}

func (r *dbReader) List(ctx context.Context, mailbox string, limit int, fuzzy bool) ([]msacl.EmailObj, error) {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	where := "LOWER(recipient) = ?"
	arg := mailbox
	if fuzzy && !strings.Contains(mailbox, "@") {
		where = "LOWER(recipient) LIKE ?"
		arg = mailbox + "%"
	}
	q := "SELECT id, recipient, subject, body_preview, verification_code, source_object_key, header_from, parsed_at " +
		"FROM inbound_mails WHERE " + where + " AND status IN ('pending','processing','stored') " +
		"ORDER BY created_at DESC, id DESC LIMIT ?"
	rows, err := r.db.QueryContext(ctx, q, arg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []msacl.EmailObj
	for rows.Next() {
		var id uint64
		var recipient, subject, preview, vcode, key, from string
		var parsedAt sql.NullTime
		if err := rows.Scan(&id, &recipient, &subject, &preview, &vcode, &key, &from, &parsedAt); err != nil {
			return nil, err
		}
		e := msacl.EmailObj{ID: id, Subject: subject, Preview: preview, VerificationCode: vcode, To: recipient, From: from}
		// If not parsed yet, pull raw .eml from MinIO and parse.
		if !parsedAt.Valid || (subject == "" && preview == "") {
			if raw, rerr := r.readObject(ctx, key); rerr == nil {
				s, f, b := parseEML(raw)
				if s != "" {
					e.Subject = s
				}
				if f != "" {
					e.From = f
				}
				e.Preview = b
			} else {
				slog.Warn("minio read failed", "key", key, "err", rerr)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *dbReader) SearchByContent(_ context.Context, _ string, _ int) ([]msacl.EmailObj, error) {
	return nil, nil // not needed for OTC login
}

func (r *dbReader) readObject(ctx context.Context, key string) ([]byte, error) {
	obj, err := r.mc.GetObject(ctx, r.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func parseEML(raw []byte) (subject, from, body string) {
	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", string(raw)
	}
	dec := new(mime.WordDecoder)
	if s, e := dec.DecodeHeader(msg.Header.Get("Subject")); e == nil {
		subject = s
	} else {
		subject = msg.Header.Get("Subject")
	}
	if f, e := dec.DecodeHeader(msg.Header.Get("From")); e == nil {
		from = f
	} else {
		from = msg.Header.Get("From")
	}
	body = readBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	return subject, from, body
}

func readBody(contentType, cte string, r io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		var htmlFallback string
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			pb := readBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			pt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			switch strings.ToLower(pt) {
			case "text/plain":
				if strings.TrimSpace(pb) != "" {
					return pb
				}
			case "text/html":
				if htmlFallback == "" {
					htmlFallback = stripHTML(pb)
				}
			}
		}
		return htmlFallback
	}
	reader := r
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		reader = quotedprintable.NewReader(r)
	}
	data, _ := io.ReadAll(reader)
	text := string(data)
	if strings.EqualFold(mediaType, "text/html") {
		text = stripHTML(text)
	}
	return text
}

var tagRE = regexp.MustCompile(`(?s)<[^>]+>`)

func stripHTML(s string) string {
	return strings.Join(strings.Fields(tagRE.ReplaceAllString(s, " ")), " ")
}

func main() {
	email := flag.String("email", "", "account email")
	password := flag.String("password", "", "account password (enables mechanism-2 password fallback)")
	binding := flag.String("binding", "", "recovery mailbox (full)")
	proxy := flag.String("proxy", "", "proxy url")
	candidates := flag.String("candidates", "", "comma-separated alias prefixes; empty = login+list only")
	mysqlDSN := flag.String("mysql", "", "mysql DSN")
	minioEndpoint := flag.String("minio", "172.18.0.2:9000", "minio endpoint")
	minioKey := flag.String("minio-key", "remail", "minio access key")
	minioSecret := flag.String("minio-secret", "", "minio secret key")
	bucket := flag.String("bucket", "remail", "minio bucket")
	bindingDomains := flag.String("binding-domains", "", "comma-separated auxiliary binding domains (SetAuxiliaryDomains); defaults to the -binding address domain")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	db, err := sql.Open("mysql", *mysqlDSN)
	if err != nil {
		fmt.Println("mysql open:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		fmt.Println("mysql ping:", err)
		os.Exit(1)
	}
	mc, err := minio.New(*minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(*minioKey, *minioSecret, ""),
		Secure: false,
	})
	if err != nil {
		fmt.Println("minio new:", err)
		os.Exit(1)
	}
	msacl.SetMailboxReader(&dbReader{db: db, mc: mc, bucket: *bucket})

	// Inject the auxiliary binding domains (production loads these from
	// domain_resources purpose=binding). Default to the -binding address domain.
	var auxDomains []string
	for _, d := range strings.Split(*bindingDomains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			auxDomains = append(auxDomains, strings.ToLower(d))
		}
	}
	if len(auxDomains) == 0 {
		if at := strings.LastIndex(*binding, "@"); at >= 0 && at+1 < len(*binding) {
			auxDomains = []string{strings.ToLower((*binding)[at+1:])}
		}
	}
	msacl.SetAuxiliaryDomains(auxDomains)

	var cands []string
	if strings.TrimSpace(*candidates) != "" {
		for _, c := range strings.Split(*candidates, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				if !strings.Contains(c, "@") {
					c += "@outlook.com"
				}
				cands = append(cands, c)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	fmt.Printf(">>> SyncAndAddExplicitAliases email=%s binding=%s candidates=%v\n", *email, *binding, cands)
	res := msacl.SyncAndAddExplicitAliases(ctx, *email, *password, *proxy, *binding, cands)
	fmt.Println("========== RESULT ==========")
	if res.OverallFailure != nil {
		fmt.Printf("OVERALL FAILURE: category=%s stage=%s msg=%s\n",
			res.OverallFailure.Category, res.OverallFailure.Stage, res.OverallFailure.SafeMessage)
	}
	fmt.Printf("ExistingAliases (%d): %v\n", len(res.ExistingAliases), res.ExistingAliases)
	for _, r := range res.AddResults {
		fmt.Printf("AddResult: aliases=%v category=%s stage=%s msg=%s\n", r.Aliases, r.Category, r.Stage, r.SafeMessage)
	}
	fmt.Println("============================")
}
