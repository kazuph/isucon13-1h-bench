CREATE TABLE icon_blobs(image_hash CHAR(64) CHARACTER SET ascii PRIMARY KEY, image LONGBLOB NOT NULL) ENGINE=InnoDB;
INSERT IGNORE INTO icon_blobs SELECT image_hash,image FROM icons;
CREATE TABLE icon_refs(id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,user_id BIGINT NOT NULL UNIQUE,image_hash CHAR(64) CHARACTER SET ascii NOT NULL) ENGINE=InnoDB;
INSERT INTO icon_refs SELECT id,user_id,image_hash FROM icons;
RENAME TABLE icons TO icons_before_deduplication, icon_refs TO icons;
