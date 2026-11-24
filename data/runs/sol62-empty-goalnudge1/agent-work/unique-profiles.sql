ALTER TABLE themes DROP INDEX themes_user, ADD UNIQUE INDEX themes_user (user_id);
ALTER TABLE icons DROP INDEX icons_user, ADD UNIQUE INDEX icons_user (user_id);
