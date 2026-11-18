ALTER TABLE icons ADD COLUMN image_hash CHAR(64) GENERATED ALWAYS AS (SHA2(image,256)) STORED;
ALTER TABLE icons ADD UNIQUE INDEX icons_unique_user (user_id);
