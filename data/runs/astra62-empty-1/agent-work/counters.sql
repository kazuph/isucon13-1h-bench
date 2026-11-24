ALTER TABLE icons ADD UNIQUE KEY one_icon(user_id);
ALTER TABLE livestreams ADD score BIGINT NOT NULL DEFAULT 0 INVISIBLE, ADD INDEX(score,id);
ALTER TABLE users ADD score BIGINT NOT NULL DEFAULT 0 INVISIBLE, ADD INDEX(score,name);
UPDATE livestreams l SET score=(SELECT COUNT(*) FROM reactions r WHERE r.livestream_id=l.id)+(SELECT COALESCE(SUM(c.tip),0) FROM livecomments c WHERE c.livestream_id=l.id);
UPDATE users u SET score=(SELECT COALESCE(SUM(l.score),0) FROM livestreams l WHERE l.user_id=u.id);
DELIMITER //
CREATE TRIGGER reaction_score AFTER INSERT ON reactions FOR EACH ROW
BEGIN
 UPDATE livestreams SET score=score+1 WHERE id=NEW.livestream_id;
 UPDATE users SET score=score+1 WHERE id=(SELECT user_id FROM livestreams WHERE id=NEW.livestream_id);
END//
CREATE TRIGGER tip_score AFTER INSERT ON livecomments FOR EACH ROW
BEGIN
 IF NEW.tip<>0 THEN
 UPDATE livestreams SET score=score+NEW.tip WHERE id=NEW.livestream_id;
 UPDATE users SET score=score+NEW.tip WHERE id=(SELECT user_id FROM livestreams WHERE id=NEW.livestream_id);
 END IF;
END//
CREATE TRIGGER remove_tip AFTER DELETE ON livecomments FOR EACH ROW
BEGIN
 IF OLD.tip<>0 THEN
 UPDATE livestreams SET score=score-OLD.tip WHERE id=OLD.livestream_id;
 UPDATE users SET score=score-OLD.tip WHERE id=(SELECT user_id FROM livestreams WHERE id=OLD.livestream_id);
 END IF;
END//
DELIMITER ;
CREATE TABLE moderated_comments LIKE livecomments;
CREATE TRIGGER archive_moderated BEFORE DELETE ON livecomments FOR EACH ROW INSERT INTO moderated_comments(id,user_id,livestream_id,comment,tip,created_at) VALUES(OLD.id,OLD.user_id,OLD.livestream_id,OLD.comment,OLD.tip,OLD.created_at);
