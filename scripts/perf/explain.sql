-- Run only against a disposable benchmark database populated by cmd/benchseed.
-- Save actual time, rows examined, chosen key, and hardware in the test report.

SET @resource_id = 1000000000;
SET @order_id = 4000000000;
SET @message_id = 6000000000;
SET @recipient = 'ms-0@bench.local';

EXPLAIN ANALYZE
SELECT ms.id AS resource_id, ms.email_address, ms.quality_score
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.alloc_bucket = MOD(@resource_id, 2048)
  AND ms.for_sale = TRUE
  AND ms.status = 'normal'
  AND u.status = 'active'
  AND u.role IN ('supplier', 'admin', 'super_admin')
	  AND NOT EXISTS (
	      SELECT 1
	      FROM microsoft_allocations history_main
	      WHERE history_main.resource_id = ms.id
	        AND history_main.project_id = 900000001
	        AND history_main.mailbox = 'main'
	  )
ORDER BY ms.last_allocated_at ASC, ms.quality_score DESC, ms.id ASC
LIMIT 8;

EXPLAIN ANALYZE
SELECT id
FROM microsoft_allocations
WHERE active_kind = 1
  AND active_project_id = 0
  AND active_entity_id = @resource_id;

EXPLAIN ANALYZE
SELECT id, sender, subject, body_preview, verification_code, received_at
FROM mailmatch_messages
WHERE matched_order_id = @order_id
  AND received_at >= UTC_TIMESTAMP() - INTERVAL 3 DAY
  AND received_at <= UTC_TIMESTAMP()
ORDER BY received_at DESC, id DESC
LIMIT 30;

EXPLAIN ANALYZE
SELECT o.id, o.order_no, o.status, o.service_mode
FROM order_tokens t
JOIN orders o ON o.order_no = t.order_no
JOIN projects p ON p.id = o.project_id
LEFT JOIN microsoft_allocations ma
  ON ma.id = o.microsoft_alloc_id AND o.allocation_type = 'microsoft'
LEFT JOIN domain_allocations da
  ON da.id = o.domain_alloc_id AND o.allocation_type = 'domain'
WHERE t.token_plain = 'st_bench_000000000000'
  AND t.enabled = 1
  AND (t.expire_at IS NULL OR t.expire_at > UTC_TIMESTAMP())
  AND (
    (o.allocation_type = 'microsoft' AND ma.order_no = o.order_no AND ma.email = @recipient)
    OR
    (o.allocation_type = 'domain' AND da.order_no = o.order_no AND da.email = @recipient)
  )
LIMIT 1;

EXPLAIN ANALYZE
SELECT order_no
FROM orders
WHERE status = 'active'
  AND service_mode = 'code'
  AND receive_until < UTC_TIMESTAMP()
ORDER BY id
LIMIT 200;

EXPLAIN ANALYZE
SELECT id
FROM mailmatch_messages
WHERE resource_type = 'microsoft'
  AND received_at < UTC_TIMESTAMP() - INTERVAL 3 DAY
ORDER BY received_at, id
LIMIT 5000;

EXPLAIN ANALYZE
SELECT id, order_no, status
FROM orders
WHERE user_id = 900000001
  AND id < 4001000000
ORDER BY id DESC
LIMIT 101;
