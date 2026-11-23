CREATE TABLE IF NOT EXISTS payments (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  payment_no VARCHAR(40) NOT NULL,
  order_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  amount BIGINT UNSIGNED NOT NULL COMMENT 'Amount in the smallest currency unit',
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  method VARCHAR(24) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  provider_trade_no VARCHAR(80) NULL,
  callback_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_payments_payment_no (payment_no),
  UNIQUE KEY uk_payments_order_id (order_id),
  KEY idx_payments_user_created (user_id, created_at),
  KEY idx_payments_status_updated (status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS payment_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_key VARCHAR(80) NOT NULL,
  payment_no VARCHAR(40) NOT NULL,
  payload JSON NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  attempts INT UNSIGNED NOT NULL DEFAULT 0,
  claim_token CHAR(36) NULL,
  claimed_until DATETIME(3) NULL,
  last_error VARCHAR(500) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  queued_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_payment_outbox_event (event_key),
  KEY idx_payment_outbox_dispatch (status, claimed_until, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
