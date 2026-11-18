ALTER TABLE isudns.records ADD INDEX records_name_type (name,type);
ALTER TABLE isupipe.icons ADD COLUMN image_hash CHAR(64) GENERATED ALWAYS AS (SHA2(image,256)) STORED;
