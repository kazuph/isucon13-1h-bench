CREATE USER IF NOT EXISTS 'isucon'@'10.99.0.%' IDENTIFIED BY 'isucon';
GRANT ALL ON isupipe.* TO 'isucon'@'10.99.0.%';
GRANT ALL ON isudns.* TO 'isucon'@'10.99.0.%';
CREATE USER IF NOT EXISTS 'isudns'@'10.99.0.%' IDENTIFIED BY 'isudns';
GRANT ALL ON isudns.* TO 'isudns'@'10.99.0.%';
SET PERSIST innodb_buffer_pool_size=2147483648;
SET PERSIST innodb_flush_log_at_trx_commit=2;
