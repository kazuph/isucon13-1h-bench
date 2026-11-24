ALTER TABLE icons DROP INDEX icon_owner, ADD UNIQUE INDEX icon_owner(user_id);
