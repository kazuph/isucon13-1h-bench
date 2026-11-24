CREATE TABLE stream_catalog_version (
    id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    revision BIGINT UNSIGNED NOT NULL
) ENGINE=InnoDB;

INSERT INTO stream_catalog_version (id, revision) VALUES (1, 0);
